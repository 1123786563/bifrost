package governance

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * Bifrost 16.1 (#322): the WebRTC entrance reuses the WebSocket entrance's unit reservation
 * and input gating — tested at the seam both relays share, the gate handle the establishment's
 * PreRequestHook attaches. There is no WebRTC-shaped second admission to test, and that is
 * the assertion: the entrance label changes the session identity the units are keyed by and
 * nothing else — the same attach point, the same closed refusal vocabulary, the same unit
 * registry, and decision-for-decision the same answers.
 *
 * The transports-side wiring (webrtcrealtime.go consulting this gate before the upstream
 * data-channel write, mirroring wsrealtime.go's order) is pinned in the transports module's
 * own suite; what this file pins is the admission machinery both entrances converge on.
 */

// worldCounter keeps every gated world's session identity unique across a whole run.
var worldCounter int

// webrtcGatedSession is one WebRTC-entrance test's world: the same attach point the WS
// upgrade uses, a session identity named for the WebRTC entrance.
func webrtcGatedSession(t *testing.T, admission *CreditAdmissionConfig) (*GovernancePlugin, *RealtimeInputGate, *schemas.BifrostContext, string) {
	t.Helper()
	plugin := newReserveTestPlugin(t, admission)
	ctx := reserveTestCtx()
	// Unique per call, not per test: one test may stand up several gates (the parity and
	// refusal-delta cases below do), and the unit registry is keyed by session id — two
	// worlds sharing an id would see each other's units.
	worldCounter++
	sessionID := fmt.Sprintf("rs-webrtc-%s-%d", t.Name(), worldCounter)
	t.Cleanup(func() { ClearRealtimeUnits(sessionID) })
	return plugin, RealtimeInputGateFromContext(attachGateLikeTheTransport(plugin, ctx, t)), ctx, sessionID
}

// attachGateLikeTheTransport attaches the gate the way BOTH transports' establishment paths
// do — the governance PreRequestHook on a RealtimeRequest (the WS upgrade and the WebRTC SDP
// route both run it before any key is selected) — so the handle under test is the one the
// relays actually read, not a test-forged one.
func attachGateLikeTheTransport(plugin *GovernancePlugin, ctx *schemas.BifrostContext, t *testing.T) *schemas.BifrostContext {
	t.Helper()
	err := plugin.PreRequestHook(ctx, &schemas.BifrostRequest{
		RequestType:      schemas.RealtimeRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: reservePaidModel},
	})
	require.NoError(t, err)
	return ctx
}

func TestWebRTCAdmissionAttachesTheSameGateTheWebSocketUpgradeAttaches(t *testing.T) {
	plugin := newReserveTestPlugin(t, admissionWithReserve("http://127.0.0.1:1/reserve", 0))
	ctx := attachGateLikeTheTransport(plugin, reserveTestCtx(), t)

	gate := RealtimeInputGateFromContext(ctx)
	require.NotNil(t, gate, "a RealtimeRequest establishment attaches the session's gate handle — the WebRTC SDP route runs the same PreRequestHook the WS upgrade does")

	// The handle is the plugin's own: the gate the relay asks is the admission's, never a
	// transport-forged one, so a WebRTC relay reading the context gets the same door.
	decision := gate.Decide(ctx, "rs-webrtc-establish", schemas.OpenAI, reservePaidModel, audioAppend(256), 350)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward, "an unreachable door answers refused — the WebRTC entrance has no optimistic branch")
	t.Cleanup(func() { ClearRealtimeUnits("rs-webrtc-establish") })
}

func TestWebRTCAdmissionUnreservedInputIsDroppedAndReservedInputForwards(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	_, gate, ctx, sessionID := webrtcGatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	// The first billed input of the WebRTC data channel opens its unit BEFORE the forward —
	// one door call, the unit's id as the idempotency key, the same closed evidence set.
	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(1024), 1400)
	require.NotNil(t, decision)
	assert.True(t, decision.Forward, "input covered by a reserved unit forwards on the data channel path")
	assert.Equal(t, 1, fake.calls())
	_, body := fake.snapshot()
	assert.Equal(t, "realtime", body["model"].(map[string]any)["capability"])
	assert.Regexp(t, `^ru-`, body["idempotencyKey"])

	units := SnapshotRealtimeUnits(sessionID)
	require.Len(t, units, 1)
	assert.Equal(t, RealtimeUnitKindInputAudio, units[0].Kind)
	assert.Equal(t, RealtimeUnitStateProcessing, units[0].State)
	assert.Equal(t, "res-reserve-1", units[0].ReservationID)

	// A refused door (here: the price basis) drops the input unbilled — nothing may be
	// written upstream that no unit covers (未预留输入不向供应商转发). A price refusal is
	// the neutral class; the authorization family's shutdown is the test below.
	refusedFake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	_, refusedGate, refusedCtx, refusedSession := webrtcGatedSession(t, admissionWithReserve(refusedFake.endpoint(), 0))
	refused := refusedGate.Decide(refusedCtx, refusedSession, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, refused)
	assert.False(t, refused.Forward)
	assert.Equal(t, RealtimeInputInterceptReserveRefused, refused.Reason)
}

func TestWebRTCAdmissionExhaustionAnswersWithTheBalanceReasonAndLatches(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("insufficient_credits"))
	_, gate, ctx, sessionID := webrtcGatedSession(t, admissionWithReserve(fake.endpoint(), 0))

	first := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, first)
	assert.False(t, first.Forward)
	assert.Equal(t, RealtimeInputInterceptReserveExhausted, first.Reason,
		"the WebRTC entrance's balance verdict carries the exhaustion reason — a money verdict, never a transport verdict")

	for i := 0; i < 2; i++ {
		again := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(128), 170)
		require.NotNil(t, again)
		assert.False(t, again.Forward)
		assert.Equal(t, RealtimeInputInterceptReserveExhausted, again.Reason)
	}
	assert.Equal(t, 1, fake.calls(), "the latch never re-asks the door")
}

func TestWebRTCAdmissionSharesTheStateMachineWithTheWebSocketEntrance(t *testing.T) {
	// The same door timeline driven through two session identities — one named for each
	// entrance — answers the SAME registry shapes: the unit machine, its states and its
	// refusal classes are the entrance's not at all (两类入口共用同一状态机).
	webrtcFake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	_, webrtcGate, webrtcCtx, webrtcSession := webrtcGatedSession(t, admissionWithReserve(webrtcFake.endpoint(), 0))
	webrtcDecision := webrtcGate.Decide(webrtcCtx, webrtcSession, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, webrtcDecision)
	assert.True(t, webrtcDecision.Forward)

	wsFake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	_, wsGate, wsCtx, wsSession := gatedSession(t, admissionWithReserve(wsFake.endpoint(), 0))
	wsDecision := wsGate.Decide(wsCtx, wsSession, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, wsDecision)
	assert.True(t, wsDecision.Forward)

	webrtcUnits := SnapshotRealtimeUnits(webrtcSession)
	wsUnits := SnapshotRealtimeUnits(wsSession)
	require.Len(t, webrtcUnits, 1)
	require.Len(t, wsUnits, 1)
	assert.Equal(t, wsUnits[0].Kind, webrtcUnits[0].Kind)
	assert.Equal(t, wsUnits[0].State, webrtcUnits[0].State)
	assert.Equal(t, wsUnits[0].BoundCents, webrtcUnits[0].BoundCents)
	assert.Equal(t, wsUnits[0].RefusalReason, webrtcUnits[0].RefusalReason)
	assert.Regexp(t, `^ru-`, webrtcUnits[0].UnitID)
	assert.Regexp(t, `^ru-`, wsUnits[0].UnitID)

	// The replay converges identically: the same unit key answers the original hold, on
	// either entrance, without a second door call (不重复派发).
	assert.Equal(t, 1, webrtcFake.calls())
	replay := webrtcGate.Decide(webrtcCtx, webrtcSession, schemas.OpenAI, reservePaidModel, audioAppend(256), 350)
	require.NotNil(t, replay)
	assert.True(t, replay.Forward)
	assert.Equal(t, 1, webrtcFake.calls(), "the budget-covered append spends the SAME unit — no second hold")
	require.Len(t, SnapshotRealtimeUnits(webrtcSession), 1)
}

func TestWebRTCAdmissionShutdownIsTheAuthorizationVerdictOnBothEntrances(t *testing.T) {
	// A revoked grant ends the session at either entrance: the decision carries Shutdown
	// (the relay closes both sides) and the registry's holding units become unknown with
	// the hold kept — the 15.6 semantics, entrance-agnostic.
	webrtcFake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("grant_invalid"))
	_, gate, ctx, sessionID := webrtcGatedSession(t, admissionWithReserve(webrtcFake.endpoint(), 0))
	decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, decision)
	assert.False(t, decision.Forward)
	assert.True(t, decision.Shutdown, "an authorization refusal tells the WebRTC relay to close both sides")
	assert.Equal(t, RealtimeInputInterceptSessionShutdown, decision.Reason)

	wsFake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("grant_invalid"))
	_, wsGate, wsCtx, wsSession := gatedSession(t, admissionWithReserve(wsFake.endpoint(), 0))
	wsDecision := wsGate.Decide(wsCtx, wsSession, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
	require.NotNil(t, wsDecision)
	assert.Equal(t, decision.Shutdown, wsDecision.Shutdown)
	assert.Equal(t, decision.Reason, wsDecision.Reason,
		"the shutdown verdict is the same closed code on both entrances")
}
