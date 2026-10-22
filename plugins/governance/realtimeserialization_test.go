package governance

import (
	"time"

	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * The strict unit serialization policy (15.4, #317): while a session's previous unit is
 * still consuming, no new unitId releases unreserved input (旧单元未结清不放开新输入).
 *
 * THE POLICY UNDER TEST — reject-first, strictly serial, observable:
 *   - a LIVE unit (holds a reservation and has budget left) keeps covering the input: a
 *     newer record without a hold never displaces it and never triggers a second hold;
 *   - a unit open already in flight (a pending unit) makes concurrent billed input REJECT
 *     (`unit_open_in_progress`) — no queue, no second door call;
 *   - a refused unit open LATCHES: later billed input is rejected under the recorded
 *     refusal without minting units or calling the door — the client's cadence cannot
 *     grow unbounded state (no buffering, bounded memory);
 *   - the policy answers uniformly for every billed input event of the closed set, and
 *     leaves non-billed events untouched;
 *   - the legitimate rollover (a spent unit's budget opening the next) still works.
 */

func TestRealtimeUnitSerializationNewUnitIdDoesNotDisplaceLiveUnit(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	// Unit A opens and holds: the first append forwards under it.
	first := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, first)
	require.True(t, first.Forward)
	assert.Equal(t, 1, fake.calls())

	// A NEWER unit id appears mid-flight without a hold — the client's concurrency. The
	// old unit is still consuming (holds, budget left): the new id must not release
	// unreserved input, must not displace the old unit, and must not spend a second hold.
	newUnitID := mintRealtimeUnitID()
	recorded := recordRealtimeUnitPending(sessionID, newUnitID, newUnitID, RealtimeUnitKindInputAudio, 100, time.Time{})
	require.NotNil(t, recorded)
	recordRealtimeUnitRefused(newUnitID, CreditRefusalInsufficientBalance, time.Time{})

	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(256), 350)
	require.NotNil(t, decision)
	assert.True(t, decision.Forward, "the input keeps flowing under the old live unit")
	assert.Equal(t, 1, fake.calls(), "a new unitId never buys a second door call while the old unit is live")

	units := SnapshotRealtimeUnits(sessionID)
	require.Len(t, units, 2, "no third unit is minted for the client's new id")
	assert.Equal(t, RealtimeUnitStateProcessing, units[0].State)
	assert.Equal(t, RealtimeUnitStateRefused, units[1].State)
	assert.Zero(t, gate.Stats().InterceptedEvents, "nothing was intercepted")
}

func TestRealtimeUnitSerializationConcurrentOpenInProgressIsRejected(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	// Another unit open is already in flight (a pending unit recorded by a concurrent
	// decision): the policy is explicit — reject, never queue, never a second open.
	pending := recordRealtimeUnitPending(sessionID, mintRealtimeUnitID(), mintRealtimeUnitID(), RealtimeUnitKindInputAudio, 100, time.Time{})
	require.NotNil(t, pending)

	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(128), 170)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward)
	assert.Equal(t, RealtimeInputInterceptOpenInProgress, decision.Reason)
	assert.Equal(t, 1, len(SnapshotRealtimeUnits(sessionID)), "no second unit is minted for the concurrent request")
	assert.Zero(t, fake.calls(), "the door is not asked while an open is already in flight")

	stats := gate.Stats()
	assert.EqualValues(t, 1, stats.InterceptedEvents)
	assert.EqualValues(t, 170, stats.InterceptedBytesByReason[RealtimeInputInterceptOpenInProgress])
}

func TestRealtimeUnitSerializationRefusedOpenLatchesWithoutNewUnitsOrDoorCalls(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	first := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(256), 350)
	require.NotNil(t, first)
	assert.False(t, first.Forward)
	assert.Equal(t, 1, fake.calls())

	// The refusal stands: later billed input is rejected under the recorded class — no
	// new unit, no new door call, no optimistic retry.
	for i := 0; i < 3; i++ {
		decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
		require.NotNil(t, decision)
		assert.False(t, decision.Forward)
		assert.Equal(t, RealtimeInputInterceptReserveRefused, decision.Reason)
	}
	assert.Equal(t, 1, fake.calls(), "the latched refusal never re-asks the door")
	assert.Len(t, SnapshotRealtimeUnits(sessionID), 1, "the client's retry cadence mints nothing")
}

func TestRealtimeUnitSerializationFloodDoesNotGrowUnboundedState(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(128), 170)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward)

	// A flood of client appends after the latch: unit records stay constant, the
	// interception log stays bounded, and the stats counters stay exact.
	for i := 0; i < 300; i++ {
		d := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
		require.NotNil(t, d)
		assert.False(t, d.Forward)
	}
	assert.Len(t, SnapshotRealtimeUnits(sessionID), 1, "no unbounded unit growth")
	assert.LessOrEqual(t, len(gate.Interceptions()), realtimeInputInterceptionLogLimit,
		"the interception log is bounded")

	stats := gate.Stats()
	assert.EqualValues(t, 301, stats.InterceptedEvents, "the counters count every event exactly")
	assert.EqualValues(t, 170+300*90, stats.InterceptedBytes)
}

func TestRealtimeUnitSerializationPolicyIsUniformForBilledInputs(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	// The policy is the shared decision path every billed input type flows through: the
	// closed set decides what is billed, the serialization decides how its money moves.
	// Two billed appends of very different shapes meet the same in-flight rejection.
	pending := recordRealtimeUnitPending(sessionID, mintRealtimeUnitID(), mintRealtimeUnitID(), RealtimeUnitKindInputAudio, 100, time.Time{})
	require.NotNil(t, pending)

	for _, bytes := range []int{16, 4096} {
		decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(bytes), bytes+20)
		require.NotNil(t, decision)
		assert.False(t, decision.Forward)
		assert.Equal(t, RealtimeInputInterceptOpenInProgress, decision.Reason)
	}

	// A non-billed event is not this gate's business in any state — nil decision.
	assert.Nil(t, gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel,
		&schemas.BifrostRealtimeEvent{Type: schemas.RTEventResponseCreate}, 500))
	assert.Zero(t, fake.calls())
}

func TestRealtimeUnitSerializationRolloverStillOpensTheNextUnit(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	admission := admissionWithReserve(fake.endpoint(), 0)
	admission.Reserve.InputCeilingTokens = 600
	_, gate, ctx, sessionID := gatedSession(t, admission)

	first := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, first)
	assert.True(t, first.Forward)

	// The spent budget still opens the NEXT unit — the serial policy never blocks the
	// legitimate rollover a settled-money path depends on.
	second := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, second)
	assert.True(t, second.Forward)
	assert.Equal(t, 2, fake.calls())
	assert.Len(t, SnapshotRealtimeUnits(sessionID), 2)
	assert.Zero(t, gate.Stats().InterceptedEvents)
}
