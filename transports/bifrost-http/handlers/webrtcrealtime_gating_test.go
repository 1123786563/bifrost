package handlers

import (
	"context"
	"testing"
	"time"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/providers/openai"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/plugins/governance"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/pion/ice/v4"
	pionlogging "github.com/pion/logging"
	"github.com/pion/transport/v5/vnet"
	"github.com/pion/webrtc/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * Bifrost 16.1/16.2 (#322/#323): the WebRTC relay's forward path consults the SAME audio
 * input gate the WebSocket relay consults, before the upstream write. What these tests pin
 * at the transports seam:
 *
 *   - the gate HANDLE rides the establishment's context into the long-lived relay context
 *     (the copy list is the only road it has — a dropped key would silently ungated the
 *     whole WebRTC entrance while the WS entrance kept working);
 *   - a denied decision writes NOTHING upstream: the input is dropped, unbilled, and the
 *     browser is answered with the same 402 wire error the WS relay sends;
 *   - a shutdown decision (the authorization verdict) closes the relay — both data
 *     channels and both peer connections — so nothing forwards or bills on afterwards;
 *   - a relay without a gate (a deployment without the governance plugin) forwards
 *     unchanged, exactly as the WS path does.
 */

func newGateTestOpenAIProvider(t *testing.T) *openai.OpenAIProvider {
	t.Helper()
	return openai.NewOpenAIProvider(&schemas.ProviderConfig{
		NetworkConfig:       schemas.NetworkConfig{BaseURL: "http://127.0.0.1:9"},
		SendBackRawResponse: true,
	}, bifrost.NewNoOpLogger())
}

// gateTestMessages is one WebRTC relay message handler over a relay whose upstream leg has
// no open channel, so a forwarded event lands in the pendingToUpstream queue — the queue
// IS the provider-received-nothing assertion.
func gateTestMessages(t *testing.T, ctx *schemas.BifrostContext) (*realtimeWebRTCMessages, *webrtcRelay) {
	t.Helper()
	relay := &webrtcRelay{handler: newFakeWebRTCHandler()}
	messages := &realtimeWebRTCMessages{
		client:      nil,
		session:     bfws.NewSession(nil),
		bifrostCtx:  ctx,
		provider:    newGateTestOpenAIProvider(t),
		providerKey: schemas.OpenAI,
		model:       "gpt-4o-realtime-preview",
	}
	return messages, relay
}

// oneBilledAppend is a serialized input_audio_buffer.append — the closed billed-input set.
func oneBilledAppend() []byte {
	return []byte(`{"type":"input_audio_buffer.append","audio":"aGVsbG8="}`)
}

func gateTestCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func TestWebRTCRealtimeRelayContextCarriesTheInputGateHandle(t *testing.T) {
	requestCtx, requestCancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer requestCancel()

	// The establishment's PreRequestHook attached the gate under this key; the relay
	// context must carry the SAME handle (identity-checked) or the forward path would
	// silently run ungated.
	gate := &governance.RealtimeInputGate{}
	requestCtx.SetValue(governance.RealtimeInputGateContextKey(), gate)

	relayCtx, relayCancel := newRealtimeRelayContext(requestCtx)
	defer relayCancel()

	assert.Same(t, gate, governance.RealtimeInputGateFromContext(relayCtx),
		"the WebRTC relay context carries the establishment's gate handle — the input gate key is in the copy list")
}

func TestWebRTCRealtimeFromBrowserDropsUnreservedInputAndAnswersTheBrowser(t *testing.T) {
	if logger == nil {
		SetLogger(&mockLogger{})
	}
	// A zero-value gate is the gate's own fail-closed shape: unbound to a plugin, it can
	// prove no unit — the exact decision an ungated session must never override.
	ctx := gateTestCtx()
	ctx.SetValue(governance.RealtimeInputGateContextKey(), &governance.RealtimeInputGate{})
	messages, relay := gateTestMessages(t, ctx)

	messages.fromBrowser(relay, webrtc.DataChannelMessage{Data: oneBilledAppend(), IsString: true})

	// Nothing was written upstream — the pending queue is empty, so the provider leg
	// received nothing to bill (未预留输入不向供应商转发).
	relay.channelMu.Lock()
	pending := len(relay.pendingToUpstream)
	relay.channelMu.Unlock()
	assert.Equal(t, 0, pending, "a denied billed input is not forwarded upstream")
}

func TestWebRTCRealtimeFromBrowserForwardsWhenNoGateIsAttached(t *testing.T) {
	if logger == nil {
		SetLogger(&mockLogger{})
	}
	messages, relay := gateTestMessages(t, gateTestCtx())

	messages.fromBrowser(relay, webrtc.DataChannelMessage{Data: oneBilledAppend(), IsString: true})

	relay.channelMu.Lock()
	pending := len(relay.pendingToUpstream)
	relay.channelMu.Unlock()
	assert.Equal(t, 1, pending, "a deployment without the governance gate forwards unchanged, like the WS relay")
}

func TestWebRTCRealtimeShutdownDecisionClosesBothSidesOfTheRelay(t *testing.T) {
	if logger == nil {
		SetLogger(&mockLogger{})
	}
	router := newGateTestVnetRouter(t)
	newPeer := func(ip string) *webrtc.PeerConnection {
		return newGateTestVnetPeer(t, router, ip)
	}
	browserPeer, relayPeer := newPeer("192.0.2.1"), newPeer("192.0.2.2")
	t.Cleanup(func() { _ = browserPeer.Close(); _ = relayPeer.Close() })

	// The browser side receives what the relay sends downstream before closing.
	received := make(chan string, 4)
	browserPeer.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnMessage(func(msg webrtc.DataChannelMessage) { received <- string(msg.Data) })
	})
	relayDC, err := relayPeer.CreateDataChannel("oai-events", nil)
	require.NoError(t, err)

	relay := &webrtcRelay{
		downstreamPC: relayPeer,
		upstreamPC:   newPeer("192.0.2.3"),
		handler:      newFakeWebRTCHandler(),
		onClose:      func() {},
	}
	relay.bindDownstreamChannel(relayDC)
	require.True(t, relay.markEstablished())
	connectGateTestPeers(t, relayPeer, browserPeer, relayDC)

	// The authorization verdict: a shutdown deny must close the relay — the browser data
	// channel and BOTH peer connections — after one final error event reaches the browser.
	applyRealtimeInputGateDeny(relay, &governance.RealtimeInputGateDecision{
		Forward:  false,
		Reason:   governance.RealtimeInputInterceptSessionShutdown,
		Detail:   "the session's authorization was refused (grant_invalid)",
		Shutdown: true,
	})

	assert.Eventually(t, func() bool {
		return relayPeer.ConnectionState() == webrtc.PeerConnectionStateClosed ||
			browserPeer.ConnectionState() == webrtc.PeerConnectionStateClosed ||
			relayDC.ReadyState() == webrtc.DataChannelStateClosed
	}, 10*time.Second, 50*time.Millisecond, "the shutdown closes the relay's peer connections and data channel")
}

// --- vnet helpers: two peers over an in-memory network, the same shape webrtcrelay_test
// uses for its drain assertions.

func newGateTestVnetRouter(t *testing.T) *vnet.Router {
	t.Helper()
	router, err := vnet.NewRouter(&vnet.RouterConfig{CIDR: "192.0.2.0/24", LoggerFactory: pionlogging.NewDefaultLoggerFactory()})
	require.NoError(t, err)
	require.NoError(t, router.Start())
	t.Cleanup(func() { _ = router.Stop() })
	return router
}

func newGateTestVnetPeer(t *testing.T, router *vnet.Router, ip string) *webrtc.PeerConnection {
	t.Helper()
	network, err := vnet.NewNet(&vnet.NetConfig{StaticIPs: []string{ip}})
	require.NoError(t, err)
	require.NoError(t, router.AddNet(network))
	settings := webrtc.SettingEngine{}
	settings.SetNet(network)
	settings.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	pc, err := webrtc.NewAPI(webrtc.WithSettingEngine(settings)).NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	return pc
}

func connectGateTestPeers(t *testing.T, relayPeer, browserPeer *webrtc.PeerConnection, relayDC *webrtc.DataChannel) {
	t.Helper()
	// The drain test's own handshake shape: gatheredSDP sets the local description while
	// waiting for candidates, so no second SetLocalDescription happens here.
	offer, err := relayPeer.CreateOffer(nil)
	require.NoError(t, err)
	require.NoError(t, browserPeer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: gatheredSDP(t, relayPeer, offer)}))
	answer, err := browserPeer.CreateAnswer(nil)
	require.NoError(t, err)
	require.NoError(t, relayPeer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: gatheredSDP(t, browserPeer, answer)}))
	require.Eventually(t, func() bool { return relayDC.ReadyState() == webrtc.DataChannelStateOpen }, 5*time.Second, 10*time.Millisecond,
		"the browser leg opens before the shutdown verdict lands")
}
