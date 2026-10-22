package governance

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * The realtime audio input gate (15.3, #316), tested at the seam the relay uses: one event
 * in, one decision out. The provider spy is the decision itself — the relay writes upstream
 * exactly when the gate says Forward — so every "not forwarded" assertion below is the
 * provider-receives-nothing assertion, and the door fake counts what the gate asked.
 *
 * The pinned behaviours:
 *   - billed audio input (the closed event set) forwards only over a HELD unit — the gate
 *     opens the unit itself, before the forward, under the unit's id as the idempotency key;
 *   - a door refusal, an unreachable door, or no door at all intercepts the input: dropped,
 *     unbilled, recorded with the closed reason and the bytes;
 *   - no client parameter reaches the decision — the event's own fields, however adversarial,
 *     cannot make an ungated unit look reserved;
 *   - non-audio events and the common cases (admission off, explicitly free models) pay
 *     nothing and forward unchanged.
 */

// gatedSession is one test's world: a plugin with the admission the test arms, a gate
// handle bound to it on a fresh context, and the session identity units are keyed by.
func gatedSession(t *testing.T, admission *CreditAdmissionConfig) (*GovernancePlugin, *RealtimeInputGate, *schemas.BifrostContext, string) {
	t.Helper()
	plugin := newReserveTestPlugin(t, admission)
	ctx := reserveTestCtx()
	plugin.attachRealtimeInputGate(ctx)
	gate := RealtimeInputGateFromContext(ctx)
	require.NotNil(t, gate, "the realtime upgrade attaches the session's gate handle")
	sessionID := "rs-gate-" + t.Name()
	t.Cleanup(func() { ClearRealtimeUnits(sessionID) })
	return plugin, gate, ctx, sessionID
}

// audioAppend is one billed input event: an input_audio_buffer.append carrying audio bytes.
func audioAppend(audioBytes int) *schemas.BifrostRealtimeEvent {
	return &schemas.BifrostRealtimeEvent{
		Type:  schemas.RTEventInputAudioAppend,
		Audio: make([]byte, audioBytes),
	}
}

func TestRealtimeInputGatingFirstAppendReservesAUnitThenForwards(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(1024), 1400)
	require.NotNil(t, decision)
	assert.True(t, decision.Forward, "the first append over a held unit forwards")

	// The gate opened the unit BEFORE the forward: one door call, under the unit's own id
	// as the idempotency key, and the unit is recorded reserved → processing.
	assert.Equal(t, 1, fake.calls())
	_, body := fake.snapshot()
	assert.Equal(t, "realtime", body["model"].(map[string]any)["capability"])
	assert.Regexp(t, `^ru-`, body["idempotencyKey"])
	units := SnapshotRealtimeUnits(sessionID)
	require.Len(t, units, 1)
	assert.Equal(t, RealtimeUnitKindInputAudio, units[0].Kind)
	assert.Equal(t, RealtimeUnitStateProcessing, units[0].State, "a forwarded append moves the unit to processing")
	assert.Equal(t, "res-reserve-1", units[0].ReservationID)

	// The second append fits the same unit's budget: no second door call.
	decision = gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, decision)
	assert.True(t, decision.Forward)
	assert.Equal(t, 1, fake.calls())
	assert.Len(t, SnapshotRealtimeUnits(sessionID), 1)
	assert.Empty(t, gate.Stats().InterceptedBytes, "nothing was intercepted")
}

func TestRealtimeInputGatingDoorRefusalInterceptsTheInput(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(2048), 2800)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward, "the provider receives nothing when the unit's reserve is refused")
	assert.Equal(t, RealtimeInputInterceptReserveRefused, decision.Reason)
	assert.Contains(t, decision.Detail, "price_unknown", "the door's own code rides the refusal")

	// The refused unit is recorded — the evidence the input never could have been billed.
	units := SnapshotRealtimeUnits(sessionID)
	require.Len(t, units, 1)
	assert.Equal(t, RealtimeUnitStateRefused, units[0].State)
	assert.Equal(t, CreditRefusalPriceUnknown, units[0].RefusalReason)
	assert.Empty(t, units[0].ReservationID)

	// And the interception is observable: one event, its bytes, its reason.
	stats := gate.Stats()
	assert.EqualValues(t, 1, stats.InterceptedEvents)
	assert.EqualValues(t, 2800, stats.InterceptedBytes)
	assert.EqualValues(t, 2800, stats.InterceptedBytesByReason[RealtimeInputInterceptReserveRefused])
}

func TestRealtimeInputGatingUnreachableDoorInterceptsTheInput(t *testing.T) {
	dead := newFakeCreditService(t, http.StatusOK, "")
	endpoint := dead.server.URL + "/reserve"
	dead.server.Close() // the door that cannot answer: treated as refused, no optimistic branch
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(endpoint, 0))

	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward)
	assert.Equal(t, RealtimeInputInterceptReserveUnreachable, decision.Reason)
	units := SnapshotRealtimeUnits(sessionID)
	require.Len(t, units, 1)
	assert.Equal(t, RealtimeUnitStateRefused, units[0].State)
	assert.Equal(t, CreditRefusalAdmissionUnreachable, units[0].RefusalReason)
	assert.EqualValues(t, 90, gate.Stats().InterceptedBytes)
}

func TestRealtimeInputGatingNoDoorWiredInterceptsTheInput(t *testing.T) {
	_, gate, ctx, sessionID := gatedSession(t, enabledAdmission())

	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward)
	assert.Equal(t, RealtimeInputInterceptUnitNotReserved, decision.Reason)
	assert.Empty(t, SnapshotRealtimeUnits(sessionID), "no unit exists when none could even be asked for")
}

func TestRealtimeInputGatingSpentBudgetOpensTheNextUnit(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	admission := admissionWithReserve(fake.endpoint(), 0)
	admission.Reserve.InputCeilingTokens = 600 // room for the first append only
	_, gate, ctx, sessionID := gatedSession(t, admission)

	first := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, first)
	assert.True(t, first.Forward)
	assert.Equal(t, 1, fake.calls())

	// The second append exceeds the unit's ceiling: the gate opens the NEXT unit before it
	// forwards — a new hold, a new unit record, still zero interceptions.
	second := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, second)
	assert.True(t, second.Forward)
	assert.Equal(t, 2, fake.calls(), "the spent budget opened a second unit before forwarding")
	assert.Len(t, SnapshotRealtimeUnits(sessionID), 2)
	assert.Zero(t, gate.Stats().InterceptedEvents)
}

func TestRealtimeInputGatingNonAudioEventsAreNotGated(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	// Control events, a turn start, a commit — the turn pipeline's own admission's
	// business: the gate answers nil (forward unchanged) without touching the door.
	for _, event := range []*schemas.BifrostRealtimeEvent{
		{Type: schemas.RTEventSessionUpdate, Session: &schemas.RealtimeSession{}},
		{Type: schemas.RTEventInputAudioCommit},
		{Type: schemas.RTEventResponseCreate},
		{Type: schemas.RTEventConversationItemCreate, Item: &schemas.RealtimeItem{}},
	} {
		assert.Nil(t, gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, event, 500),
			"a %s event is not billed audio input", event.Type)
	}
	assert.Zero(t, fake.calls())
	assert.Empty(t, SnapshotRealtimeUnits(sessionID))
}

func TestRealtimeInputGatingClientCannotBypassByParameters(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	// An append whose own fields claim everything a bypass could wish for — a reservation,
	// a payment account, an authorization, a budget — is still one billed input event, and
	// the decision reads none of it: the unit registry and the door are the only inputs.
	adversarial := audioAppend(256)
	adversarial.EventID = "evt-claiming-reservation-cr-123"
	adversarial.ExtraParams = map[string]json.RawMessage{
		"reservation_id": json.RawMessage(`"cr-123"`),
		"payer":          json.RawMessage(`{"kind":"personal","id":"someone-else"}`),
		"grant_id":       json.RawMessage(`"grant-forged"`),
		"free":           json.RawMessage("true"),
	}
	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, adversarial, 350)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward, "no event field makes an ungated unit look reserved")
	assert.Equal(t, RealtimeInputInterceptReserveRefused, decision.Reason)
	assert.Equal(t, CreditRefusalPriceUnknown, SnapshotRealtimeUnits(sessionID)[0].RefusalReason)
}

func TestRealtimeInputGatingAdmissionDisabledForwardsWithoutUnits(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	// The extension is off entirely: the gate is part of it, so the common path pays
	// nothing — no unit, no door call, no interception record.
	plugin := newReserveTestPlugin(t, &CreditAdmissionConfig{})
	ctx := reserveTestCtx()
	plugin.attachRealtimeInputGate(ctx)
	gate := RealtimeInputGateFromContext(ctx)
	require.NotNil(t, gate)

	decision := gate.Decide(ctx, "rs-gate-off", schemas.OpenAI, reservePaidModel, audioAppend(1024), 1400)
	require.NotNil(t, decision)
	assert.True(t, decision.Forward)
	assert.Zero(t, fake.calls())
	assert.Empty(t, SnapshotRealtimeUnits("rs-gate-off"))
}

func TestRealtimeInputGatingExplicitlyFreeModelNeedsNoUnit(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	// The catalog prices this model explicitly free — the funnel's own free semantics:
	// no unit, no door call, the input forwards.
	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reserveFreeModel, audioAppend(1024), 1400)
	require.NotNil(t, decision)
	assert.True(t, decision.Forward)
	assert.Zero(t, fake.calls())
	assert.Empty(t, SnapshotRealtimeUnits(sessionID))
}

func TestRealtimeInputGateRecordsAreObservableAndCopied(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	for i := 0; i < 3; i++ {
		decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(128), 170)
		require.NotNil(t, decision)
		assert.False(t, decision.Forward)
	}

	records := gate.Interceptions()
	require.Len(t, records, 3)
	assert.Equal(t, RealtimeInputInterceptReserveRefused, records[0].Reason)
	assert.EqualValues(t, 170, records[0].Bytes)

	// The records answer is a copy: mutating it cannot rewrite the observability base.
	records[0].Reason = "rewritten"
	assert.Equal(t, RealtimeInputInterceptReserveRefused, gate.Interceptions()[0].Reason)

	stats := gate.Stats()
	assert.EqualValues(t, 3, stats.InterceptedEvents)
	assert.EqualValues(t, 510, stats.InterceptedBytes)
	assert.EqualValues(t, 510, stats.InterceptedBytesByReason[RealtimeInputInterceptReserveRefused])
}

/*
 * The exhaustion answer (15.5, #318): a balance refusal is its OWN closed interception
 * reason — distinguishable from a transport failure and from every other door refusal on
 * the stats surface, and standing (latched) so no later billed input of the session
 * re-asks the door or forwards (耗尽后不开始新的付费单元 / 耗尽原因与网络错误可区分).
 */

func TestRealtimeInputGatingExhaustionIsItsOwnReason(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("insufficient_credits"))
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward)
	assert.Equal(t, RealtimeInputInterceptReserveExhausted, decision.Reason,
		"a balance refusal answers with the exhaustion reason, not the generic refusal")
	assert.Contains(t, decision.Detail, "insufficient_credits")

	units := SnapshotRealtimeUnits(sessionID)
	require.Len(t, units, 1)
	assert.Equal(t, CreditRefusalInsufficientBalance, units[0].RefusalReason)
	assert.EqualValues(t, 700, gate.Stats().InterceptedBytesByReason[RealtimeInputInterceptReserveExhausted])
}

func TestRealtimeInputGatingExhaustionLatchesWithoutReasking(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("insufficient_credits"))
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	first := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, first)
	assert.Equal(t, RealtimeInputInterceptReserveExhausted, first.Reason)

	for i := 0; i < 2; i++ {
		decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(128), 170)
		require.NotNil(t, decision)
		assert.False(t, decision.Forward)
		assert.Equal(t, RealtimeInputInterceptReserveExhausted, decision.Reason,
			"the latched exhaustion keeps answering under its own reason")
	}
	assert.Equal(t, 1, fake.calls(), "the exhausted session never re-asks the door")
	assert.Len(t, SnapshotRealtimeUnits(sessionID), 1)
}

func TestRealtimeInputGatingExhaustionUnreachableAndOtherRefusalsStayDistinct(t *testing.T) {
	// A transport failure answers unreachable — never exhaustion.
	dead := newFakeCreditService(t, http.StatusOK, "")
	endpoint := dead.server.URL + "/reserve"
	dead.server.Close()
	_, unreachableGate, ctx, sid1 := gatedSession(t, admissionWithReserve(endpoint, 0))
	t.Cleanup(func() { ClearRealtimeUnits(sid1) })
	decision := unreachableGate.Decide(ctx, sid1, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, decision)
	assert.Equal(t, RealtimeInputInterceptReserveUnreachable, decision.Reason)
	assert.NotEqual(t, RealtimeInputInterceptReserveExhausted, decision.Reason)

	// Another door refusal (a price verdict) answers the generic refused — distinct from
	// both the exhaustion and the transport class.
	refused := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	_, refusedGate, ctx2, sid2 := gatedSession(t, admissionWithReserve(refused.endpoint(), 0))
	// A distinct session id: the legs are two sessions, and one leg's latch must never
	// answer for the other's.
	sid2 += "-price"
	t.Cleanup(func() { ClearRealtimeUnits(sid2) })
	decision = refusedGate.Decide(ctx2, sid2, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, decision)
	assert.Equal(t, RealtimeInputInterceptReserveRefused, decision.Reason)
	assert.NotEqual(t, RealtimeInputInterceptReserveExhausted, decision.Reason)
}
