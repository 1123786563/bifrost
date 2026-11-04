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
 * The best-effort shutdown (15.6, #319): an authorization verdict the session can no
 * longer stand on (授权撤销 / Key 到期 — one family at the door) ends the session on the
 * gateway side too — the still-holding units become unknown in the registry, every later
 * event of the session answers SHUTDOWN so the relay closes BOTH connections (关闭上游
 * 连接与流), and the shutdown is observable. A balance verdict or a price verdict never
 * shuts down: those stop input (15.4/15.5) without tearing the session down.
 */

// holdingInputUnit records a live holding input unit for the session the way a successful
// open would have — the unit the shutdown sweep must unknown.
func holdingInputUnit(t *testing.T, sessionID string) string {
	t.Helper()
	unitID := mintRealtimeUnitID()
	recorded := recordRealtimeUnitPending(sessionID, unitID, unitID, RealtimeUnitKindInputAudio, 100, time.Time{})
	require.NotNil(t, recorded)
	recordRealtimeUnitReserved(unitID, "res-live-1", time.Time{})
	return unitID
}

func TestRealtimeUnitShutdownAuthorizationRefusalEndsTheSession(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("grant_invalid"))
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))
	liveUnitID := holdingInputUnit(t, sessionID)

	// An append too large for the live unit's ceiling forces the NEXT unit open, which
	// the door refuses with the authorization verdict.
	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(1<<20), 1<<21)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward)
	assert.True(t, decision.Shutdown, "an authorization refusal shuts the session down")

	// The registry's holding unit became unknown with the shutdown's own reason — kept,
	// not released, exactly what 15.7's tail settlement will read.
	units := SnapshotRealtimeUnits(sessionID)
	live := units[0]
	assert.Equal(t, liveUnitID, live.UnitID)
	assert.Equal(t, RealtimeUnitStateUnknown, live.State)
	assert.Equal(t, "shutdown_tail_unconfirmed", live.UnknownReason)
	assert.NotEmpty(t, live.ReservationID, "the hold is kept — the shutdown releases nothing")

	assert.Equal(t, "grant_invalid", gate.ShutdownReason())
}

func TestRealtimeUnitShutdownLatchesForEveryEventAndClosesBothSides(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("payer_conflict"))
	_, gate, ctx, sessionID := gatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	first := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, first)
	require.True(t, first.Shutdown)

	// Every later event — billed input AND a control event — answers shutdown: the relay
	// closes the upstream connection and stops forwarding on this verdict, which is the
	// "尽力关停" half only the relay can perform (the transport test pins the wiring).
	for _, event := range []*schemas.BifrostRealtimeEvent{
		audioAppend(128),
		{Type: schemas.RTEventSessionUpdate, Session: &schemas.RealtimeSession{}},
		{Type: schemas.RTEventResponseCreate},
	} {
		decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, event, 170)
		require.NotNil(t, decision, "a shutdown session answers every event")
		assert.False(t, decision.Forward)
		assert.True(t, decision.Shutdown)
		assert.Equal(t, RealtimeInputInterceptSessionShutdown, decision.Reason)
	}
	assert.Equal(t, 1, fake.calls(), "the latched shutdown never re-asks the door")

	stats := gate.Stats()
	// Four interceptions: the refused open itself (90 bytes) plus the three latched
	// events (the billed append's 170; the control frames carry no billable bytes).
	assert.EqualValues(t, 4, stats.InterceptedEvents)
	assert.EqualValues(t, 90+170, stats.InterceptedBytesByReason[RealtimeInputInterceptSessionShutdown])
}

func TestRealtimeUnitShutdownNonAuthorizationVerdictsDoNotTearTheSessionDown(t *testing.T) {
	// A balance verdict stops input (15.5) but never shuts the session down: the stop is
	// not a close, and the client's session may drain its settled money.
	exhausted := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("insufficient_credits"))
	_, exhaustedGate, ctx, sid1 := gatedSession(t, admissionWithReserve(exhausted.endpoint(), 0))
	sid1 += "-bal"
	t.Cleanup(func() { ClearRealtimeUnits(sid1) })
	decision := exhaustedGate.Decide(ctx, sid1, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward)
	assert.False(t, decision.Shutdown, "exhaustion stops; it does not close")

	// A price verdict is a posture answer, not an authorization one.
	priced := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	_, pricedGate, ctx2, sid2 := gatedSession(t, admissionWithReserve(priced.endpoint(), 0))
	sid2 += "-price"
	t.Cleanup(func() { ClearRealtimeUnits(sid2) })
	decision = pricedGate.Decide(ctx2, sid2, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, decision)
	assert.False(t, decision.Shutdown, "a price refusal latches but does not shut down")
	assert.Empty(t, pricedGate.ShutdownReason())
}
