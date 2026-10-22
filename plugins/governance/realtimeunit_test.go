package governance

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * The realtime billing unit registry (15.2, #315): the gateway-side bookkeeping of which
 * unit a realtime session is under, and what became of it. Two layers are pinned:
 *
 *   - the registry itself: a unit is born pending, ends reserved (with the hold the door
 *     answered) or refused (with the refusing check's own code), moves reserved → processing
 *     under the guarded transition, and answers per-kind lookups and snapshots as copies;
 *   - the reservation hook: a REALTIME admission through the funnel records a response unit
 *     for the session with a terminal outcome — today that outcome is honestly `refused`
 *     (the realtime call method has no provable bound evidence yet, so the funnel refuses
 *     the turn before the door is ever asked), which is exactly the unit row the 15.3 input
 *     gate and the observability reads consume. A non-realtime admission records nothing.
 */

func TestRealtimeUnitRegistryFlowsReserved(t *testing.T) {
	sessionID := "rs-registry-reserved"
	t.Cleanup(func() { ClearRealtimeUnits(sessionID) })

	unitID := mintRealtimeUnitID()
	recorded := recordRealtimeUnitPending(sessionID, unitID, "att-registry-1", RealtimeUnitKindInputAudio, 500, time.Now())
	require.NotNil(t, recorded)
	assert.Equal(t, RealtimeUnitStatePending, recorded.State)
	assert.Equal(t, int64(500), recorded.BoundCents)
	assert.Empty(t, recorded.ReservationID, "a pending unit holds nothing")

	recordRealtimeUnitReserved(unitID, "res-registry-1", time.Now())
	unit := RealtimeUnitFor(sessionID, RealtimeUnitKindInputAudio)
	require.NotNil(t, unit)
	assert.Equal(t, RealtimeUnitStateReserved, unit.State)
	assert.Equal(t, "res-registry-1", unit.ReservationID)
	assert.Equal(t, "att-registry-1", unit.AttemptID)
	assert.NotEmpty(t, unit.ReservedAt)

	// The guarded transition: reserved → processing moves once, then refuses to move again.
	moved := MarkRealtimeUnitProcessing(sessionID, unitID)
	require.NotNil(t, moved)
	assert.Equal(t, RealtimeUnitStateProcessing, moved.State)
	assert.Equal(t, "res-registry-1", moved.ReservationID, "processing keeps the hold it reserved")
	again := MarkRealtimeUnitProcessing(sessionID, unitID)
	require.NotNil(t, again)
	assert.Equal(t, RealtimeUnitStateProcessing, again.State, "a processing unit does not move twice")
}

func TestRealtimeUnitRegistryFlowsRefused(t *testing.T) {
	sessionID := "rs-registry-refused"
	t.Cleanup(func() { ClearRealtimeUnits(sessionID) })

	unitID := mintRealtimeUnitID()
	recordRealtimeUnitPending(sessionID, unitID, "att-registry-2", RealtimeUnitKindResponse, 300, time.Now())
	recordRealtimeUnitRefused(unitID, "insufficient_balance", time.Now())

	unit := RealtimeUnitFor(sessionID, RealtimeUnitKindResponse)
	require.NotNil(t, unit)
	assert.Equal(t, RealtimeUnitStateRefused, unit.State)
	assert.Equal(t, "insufficient_balance", unit.RefusalReason)
	assert.Empty(t, unit.ReservationID, "a refused unit holds nothing — the record is the evidence")

	// A refused unit never reaches processing.
	moved := MarkRealtimeUnitProcessing(sessionID, unitID)
	require.NotNil(t, moved)
	assert.Equal(t, RealtimeUnitStateRefused, moved.State)
}

func TestRealtimeUnitRegistryShapesAndLookups(t *testing.T) {
	sessionID := "rs-registry-shapes"
	t.Cleanup(func() { ClearRealtimeUnits(sessionID) })

	// A kind outside the closed set is not a unit this registry can name.
	assert.Nil(t, recordRealtimeUnitPending(sessionID, "ru-bad-kind", "att-x", RealtimeUnitKind("vibes"), 100, time.Now()))
	// Neither is a unit without a session or an identity.
	assert.Nil(t, recordRealtimeUnitPending("", "ru-no-session", "att-x", RealtimeUnitKindInputAudio, 100, time.Now()))
	assert.Nil(t, recordRealtimeUnitPending(sessionID, "", "att-x", RealtimeUnitKindInputAudio, 100, time.Now()))

	// The unit id prefix mirrors the server's own `ru-` shape.
	assert.Regexp(t, `^ru-[0-9a-f]{32}$`, mintRealtimeUnitID())

	// Two units of different kinds, one after the other: the per-kind lookup answers the
	// newest of ITS kind, and the snapshot answers the whole history oldest first.
	first := mintRealtimeUnitID()
	recordRealtimeUnitPending(sessionID, first, "att-1", RealtimeUnitKindInputAudio, 100, time.Now())
	second := mintRealtimeUnitID()
	recordRealtimeUnitPending(sessionID, second, "att-2", RealtimeUnitKindResponse, 200, time.Now())
	third := mintRealtimeUnitID()
	recordRealtimeUnitPending(sessionID, third, "att-3", RealtimeUnitKindInputAudio, 300, time.Now())
	recordRealtimeUnitReserved(third, "res-3", time.Now())

	assert.Equal(t, third, RealtimeUnitFor(sessionID, RealtimeUnitKindInputAudio).UnitID)
	assert.Equal(t, second, RealtimeUnitFor(sessionID, RealtimeUnitKindResponse).UnitID)
	assert.Nil(t, RealtimeUnitFor(sessionID, RealtimeUnitKindDuration))

	snapshot := SnapshotRealtimeUnits(sessionID)
	require.Len(t, snapshot, 3)
	assert.Equal(t, []string{first, second, third}, []string{snapshot[0].UnitID, snapshot[1].UnitID, snapshot[2].UnitID})

	// A returned record is a copy: mutating it cannot rewrite the registry's truth — the
	// newest input unit stays reserved, and the first unit's pending state is untouched.
	snapshot[0].State = RealtimeUnitStateSettled
	assert.Equal(t, RealtimeUnitStateReserved, RealtimeUnitFor(sessionID, RealtimeUnitKindInputAudio).State)
	freshSnapshot := SnapshotRealtimeUnits(sessionID)
	assert.Equal(t, RealtimeUnitStatePending, freshSnapshot[0].State)

	ClearRealtimeUnits(sessionID)
	assert.Nil(t, RealtimeUnitFor(sessionID, RealtimeUnitKindInputAudio))
	assert.Nil(t, SnapshotRealtimeUnits(sessionID))
}

func TestRealtimeUnitSessionIDResolution(t *testing.T) {
	assert.Empty(t, RealtimeUnitSessionID(nil))

	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	assert.Empty(t, RealtimeUnitSessionID(ctx), "a context naming no session keys no units")

	// The provider's own session id wins when it has answered; otherwise the relay's
	// session identity (the parent request id the turn context carries) is the key.
	ctx.SetValue(schemas.BifrostContextKeyParentRequestID, "rs-relay-1")
	assert.Equal(t, "rs-relay-1", RealtimeUnitSessionID(ctx))
	ctx.SetValue(schemas.BifrostContextKeyRealtimeSessionID, "sess-provider-1")
	assert.Equal(t, "sess-provider-1", RealtimeUnitSessionID(ctx))
}

func TestRealtimeTurnAdmissionRecordsTheResponseUnit(t *testing.T) {
	// The door fake stands in for OpenBot's reserve surface; for a realtime turn it is
	// never asked (the funnel refuses the call method before the door — no bound evidence
	// exists for realtime yet), and the test pins exactly that honest outcome: the turn's
	// unit is recorded REFUSED with the funnel's own code, the door is called zero times,
	// and the request is turned away.
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	sessionID := "rs-turn-admission"
	t.Cleanup(func() { ClearRealtimeUnits(sessionID) })

	ctx := reserveTestCtx()
	ctx.SetValue(schemas.BifrostContextKeyParentRequestID, sessionID)
	req := &schemas.BifrostRequest{
		RequestType: schemas.RealtimeRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    reservePaidModel,
		},
	}

	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "a realtime turn without provable bound evidence is turned away")
	require.NotNil(t, shortCircuit.Error)

	assert.Equal(t, 0, fake.calls(), "the door is never asked for a turn it would have to refuse")

	units := SnapshotRealtimeUnits(sessionID)
	require.Len(t, units, 1, "the turn is one response unit")
	assert.Equal(t, RealtimeUnitKindResponse, units[0].Kind)
	assert.Equal(t, RealtimeUnitStateRefused, units[0].State)
	assert.Equal(t, CreditRefusalCapNotProvable, units[0].RefusalReason)
	assert.Empty(t, units[0].ReservationID)
}

func TestRealtimeTurnAdmissionRecordsNothingWithoutASession(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))

	// No session identity on the context: the admission proceeds (and refuses, as above)
	// but no unit is recorded — a decision about nothing is never a unit's fact.
	ctx := reserveTestCtx()
	req := &schemas.BifrostRequest{
		RequestType: schemas.RealtimeRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    reservePaidModel,
		},
	}
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, shortCircuit)
	assert.Empty(t, RealtimeUnitSessionID(ctx), "the context still names no session, so no unit was recorded")
}

func TestNonRealtimeAdmissionsRecordNoUnits(t *testing.T) {
	// The 15.2 hook is scoped to realtime admissions: a chat reserve (the 07.x path) holds
	// its reservation on the request context exactly as before, and the session registry
	// stays empty — the chat path's money is not unit money.
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	sessionID := "rs-chat-not-a-unit"
	t.Cleanup(func() { ClearRealtimeUnits(sessionID) })

	ctx := reserveTestCtx()
	ctx.SetValue(schemas.BifrostContextKeyParentRequestID, sessionID)
	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "the chat reserve holds and the request continues")
	require.NotNil(t, ctx.Value(CreditReservationContextKey), "the chat path's reservation rides the context as before")
	assert.Nil(t, SnapshotRealtimeUnits(sessionID), "no realtime unit exists for a chat admission")
}
