package governance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * Bifrost 16.2 (#323): the WebRTC entrance's shutdown and tail semantics ARE the WebSocket
 * entrance's — one state machine, one registry, one sweep. The tests here drive the SAME
 * timeline through two session identities (one named for each entrance) and assert the
 * registry's answers agree field for field: an authorization verdict shuts the session down,
 * every still-holding unit becomes unknown with the hold KEPT (the recovery's tail to settle),
 * and every later input of the session answers the shutdown latch without re-asking the
 * door. The money side — the tail settling once, within the bound, through the same settle
 * door — is the server's own fence and is pinned by server/tests/realtime-webrtc-parity
 * over both entrances' sessions.
 */

var webRTCTailCounter int

// reserveAnswer is one scripted answer of the door the timeline drives: a status and a body.
type reserveAnswerScript struct {
	status int
	body   string
}

// sequenceReserveDoor is a door fake that answers in order, once per call, then keeps
// answering the last: the first unit's open takes its hold (201), the next unit's open is
// the authorization refusal (422 grant_invalid) — one door, one relay, both answers.
func sequenceReserveDoor(t *testing.T, answers ...reserveAnswerScript) (string, *atomic.Int32) {
	t.Helper()
	var mu sync.Mutex
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&struct{}{})
		mu.Lock()
		index := int(calls.Add(1)) - 1
		if index >= len(answers) {
			index = len(answers) - 1
		}
		status, body := answers[index].status, answers[index].body
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/reserve", &calls
}

// oneTailSession stands up one entrance-named session over the scripted door: a plugin, a
// gate attached the way both transports attach it, and a session identity named for the
// entrance under test.
func oneTailSession(t *testing.T, entrance string, answers ...reserveAnswerScript) (*RealtimeInputGate, *schemas.BifrostContext, string, *atomic.Int32) {
	t.Helper()
	endpoint, calls := sequenceReserveDoor(t, answers...)
	admission := admissionWithReserve(endpoint, 0)
	// Room for the first append only (the sibling gating tests' own calibration): the
	// second billed input must roll over to the NEXT unit open, which is where the
	// timeline's refusal lands.
	admission.Reserve.InputCeilingTokens = 600
	plugin := newReserveTestPlugin(t, admission)
	ctx := reserveTestCtx()
	require.NoError(t, plugin.PreRequestHook(ctx, &schemas.BifrostRequest{
		RequestType:      schemas.RealtimeRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{Provider: schemas.OpenAI, Model: reservePaidModel},
	}))
	gate := RealtimeInputGateFromContext(ctx)
	require.NotNil(t, gate)
	webRTCTailCounter++
	sessionID := "rs-" + entrance + "-tail-" + t.Name() + "-" + string(rune('a'+webRTCTailCounter%26))
	t.Cleanup(func() { ClearRealtimeUnits(sessionID) })
	return gate, ctx, sessionID, calls
}

// The one timeline both entrances run: a unit takes its hold and starts consuming; the next
// unit's open meets the authorization refusal, which shuts the session down.
var tailTimeline = []reserveAnswerScript{
	{http.StatusCreated, `{"attemptId":"att-tail-1","reservationId":"res-tail-1","maximumCredits":100,"snapshotVersion":7,"rateVersion":3}`},
	{http.StatusUnprocessableEntity, `{"error":"refused","code":"grant_invalid","detail":{}}`},
}

func TestWebRTCTailShutdownUnknownsHoldingUnitsAndKeepsTheHold(t *testing.T) {
	gate, ctx, sessionID, _ := oneTailSession(t, "webrtc", tailTimeline...)

	first := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, first)
	require.True(t, first.Forward, "the first unit's input forwards under its hold")

	second := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, second)
	require.False(t, second.Forward)
	require.True(t, second.Shutdown, "the authorization verdict ends the WebRTC session too")

	// The sweep: the consuming unit is unknown — the shutdown could not confirm its tail —
	// and its HOLD is kept (the reservation id stays on the row), never released and never
	// silently charged (无法确认的尾部保持预留).
	units := SnapshotRealtimeUnits(sessionID)
	require.Len(t, units, 2)
	holding := 0
	for _, unit := range units {
		if unit.State == RealtimeUnitStateProcessing {
			t.Fatalf("a shutdown left a unit consuming: %+v", unit)
		}
		if unit.State == RealtimeUnitStateUnknown {
			holding++
			assert.Equal(t, "shutdown_tail_unconfirmed", unit.UnknownReason)
			assert.Equal(t, "res-tail-1", unit.ReservationID, "the unconfirmed tail's hold is kept for the settlement")
		}
	}
	assert.Equal(t, 1, holding, "exactly the holding unit became unknown")
}

func TestWebRTCTailEveryLaterEventAnswersTheShutdownLatchWithoutReasking(t *testing.T) {
	gate, ctx, sessionID, calls := oneTailSession(t, "webrtc", tailTimeline...)

	first := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, first)
	require.True(t, first.Forward)
	second := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, second)
	require.True(t, second.Shutdown)
	require.Equal(t, int32(2), calls.Load())

	for i := 0; i < 3; i++ {
		decision := gate.Decide(ctx, sessionID, schemas.OpenAI, reservePaidModel, audioAppend(64), 90)
		require.NotNil(t, decision)
		assert.False(t, decision.Forward)
		assert.True(t, decision.Shutdown)
		assert.Equal(t, RealtimeInputInterceptSessionShutdown, decision.Reason,
			"every later event of the shut-down session answers the latch — billed or not")
	}
	assert.Equal(t, int32(2), calls.Load(), "the latch never re-asks the door")
}

func TestWebRTCTailParityWithTheWebSocketEntrance(t *testing.T) {
	// The same timeline, one session identity per entrance: the registry's answers agree
	// on every field the state machine owns (kind, state, unknown reason, hold kept).
	webrtcGate, webrtcCtx, webrtcSession, _ := oneTailSession(t, "webrtc", tailTimeline...)
	webrtcFirst := webrtcGate.Decide(webrtcCtx, webrtcSession, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, webrtcFirst)
	require.True(t, webrtcFirst.Forward)
	webrtcSecond := webrtcGate.Decide(webrtcCtx, webrtcSession, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, webrtcSecond)
	require.True(t, webrtcSecond.Shutdown)
	webrtcUnits := SnapshotRealtimeUnits(webrtcSession)

	wsGate, wsCtx, wsSession, _ := oneTailSession(t, "ws", tailTimeline...)
	wsFirst := wsGate.Decide(wsCtx, wsSession, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, wsFirst)
	require.True(t, wsFirst.Forward)
	wsSecond := wsGate.Decide(wsCtx, wsSession, schemas.OpenAI, reservePaidModel, audioAppend(512), 700)
	require.NotNil(t, wsSecond)
	assert.Equal(t, webrtcSecond.Reason, wsSecond.Reason,
		"the shutdown verdict carries the same closed code on both entrances")
	wsUnits := SnapshotRealtimeUnits(wsSession)

	require.Equal(t, len(wsUnits), len(webrtcUnits), "both entrances leave the same number of unit rows")
	for i := range wsUnits {
		assert.Equal(t, wsUnits[i].Kind, webrtcUnits[i].Kind)
		assert.Equal(t, wsUnits[i].State, webrtcUnits[i].State)
		assert.Equal(t, wsUnits[i].UnknownReason, webrtcUnits[i].UnknownReason)
		assert.Equal(t, wsUnits[i].RefusalReason, webrtcUnits[i].RefusalReason)
		assert.Equal(t, wsUnits[i].ReservationID != "", webrtcUnits[i].ReservationID != "",
			"the hold-kept shape agrees between entrances")
	}
}
