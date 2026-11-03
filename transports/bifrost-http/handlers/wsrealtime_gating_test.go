package handlers

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fasthttp/router"
	ws "github.com/fasthttp/websocket"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/maximhq/bifrost/plugins/governance"
	"github.com/maximhq/bifrost/transports/bifrost-http/lib"
	bfws "github.com/maximhq/bifrost/transports/bifrost-http/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/valyala/fasthttp"
)

/*
 * Bifrost 15.3 (#316): the WebSocket entrance's audio input gate, driven end to end over
 * REAL WebSocket connections — the browser leg into the gateway, and the gateway leg out to
 * a provider spy that records every frame it actually receives.
 *
 * The provider spy is the assertion the ticket asks for: an unreserved billed input must
 * arrive there zero times. The client's own frames are the only thing that can write
 * upstream, so "the spy received nothing" is "the provider was never sent billable audio".
 *
 * What these tests pin at the transports boundary, not just at the gate's own seam:
 *
 *   - the establishment's gate handle reaches the long-lived relay context through the
 *     real WS upgrade (the handle is attached by the pre-request pipeline and must ride
 *     the middleware value snapshot into the session — a dropped key silently disables
 *     the whole entrance while the gate's unit tests stay green);
 *   - a denied append writes nothing upstream and answers the client in band;
 *   - no event field a client can put on the wire makes an ungated unit look reserved;
 *   - the positive control: over a HELD unit (a fake reserve door answering 201), the
 *     same append DOES reach the provider — the gate changes nothing for reserved units.
 */

// gateTestProviderSpy is a stand-in realtime provider: it accepts the gateway's upstream
// WebSocket and records every text frame the gateway writes.
type gateTestProviderSpy struct {
	frames chan []byte
	conns  chan *ws.Conn
}

// newGateTestProviderSpy starts a provider that speaks the OpenAI realtime WebSocket path
// and answers the gateway's /v1/realtime dial. It returns the spy and the http base URL the
// test account must point its OpenAI provider at.
func newGateTestProviderSpy(t *testing.T) (*gateTestProviderSpy, string) {
	t.Helper()
	spy := &gateTestProviderSpy{
		frames: make(chan []byte, 16),
		conns:  make(chan *ws.Conn, 4),
	}
	upgrader := ws.FastHTTPUpgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin:     func(*fasthttp.RequestCtx) bool { return true },
	}
	r := router.New()
	r.GET("/v1/realtime", func(ctx *fasthttp.RequestCtx) {
		_ = upgrader.Upgrade(ctx, func(conn *ws.Conn) {
			defer conn.Close()
			spy.conns <- conn
			for {
				_, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				spy.frames <- append([]byte(nil), data...)
			}
		})
	})
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &fasthttp.Server{Handler: r.Handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Shutdown()
	})
	return spy, "http://" + ln.Addr().String()
}

// gateTestAccount is the test account whose OpenAI provider points at the provider spy.
type gateTestAccount struct {
	wsSpanTestAccount
	baseURL string
}

func (a gateTestAccount) GetConfigForProvider(p schemas.ModelProvider) (*schemas.ProviderConfig, error) {
	cfg, err := a.wsSpanTestAccount.GetConfigForProvider(p)
	if err != nil {
		return nil, err
	}
	cfg.NetworkConfig.BaseURL = a.baseURL
	cfg.NetworkConfig.AllowPrivateNetwork = true
	return cfg, nil
}

// gateTestReserveDoor is a fake credit reserve door answering every call with a HELD
// reservation (201 + the answer body the plugin reads as a hold), counting the calls.
type gateTestReserveDoor struct {
	server   *httptest.Server
	endpoint string
	calls    atomic.Int32
}

func newGateTestReserveDoor(t *testing.T) *gateTestReserveDoor {
	t.Helper()
	door := &gateTestReserveDoor{}
	door.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		door.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"attemptId":"att-ws-1","reservationId":"res-ws-1","maximumCredits":100,"snapshotVersion":7,"rateVersion":3}`))
	}))
	t.Cleanup(door.server.Close)
	door.endpoint = door.server.URL + "/reserve"
	return door
}

// gateTestLogger installs the package logger exactly once for this suite: SetLogger writes a
// package-level global that the relay goroutines of an already-started test read concurrently,
// so a per-gateway SetLogger would race with a session still winding down.
var gateTestLogger sync.Once

// gateTestGateway is one running gateway: a real fasthttp server exposing the realtime
// route, the provider spy behind it, and the governance plugin whose gate must decide.
type gateTestGateway struct {
	client *bifrost.Bifrost
	spy    *gateTestProviderSpy
	door   *gateTestReserveDoor
	base   string
}

// newGateTestGateway starts the gateway with credit admission ON. With reserve == nil no
// reserve door is wired: every billed audio unit is unreserved, which is the fail-closed
// state the gate must enforce before any input reaches the provider. With a door wired the
// gate can hold a unit, which is the reserved-units positive control.
func newGateTestGateway(t *testing.T, reserve *governance.CreditReserveConfig, door *gateTestReserveDoor) *gateTestGateway {
	t.Helper()
	gateTestLogger.Do(func() { SetLogger(&mockLogger{}) })

	spy, spyURL := newGateTestProviderSpy(t)
	log := bifrost.NewDefaultLogger(schemas.LogLevelError)
	isVkMandatory := false
	admissionEnabled := true
	plugin, err := governance.Init(t.Context(), &governance.Config{
		IsVkMandatory: &isVkMandatory,
		OpenBotCreditAdmission: &governance.CreditAdmissionConfig{
			Enabled: &admissionEnabled,
			Reserve: reserve,
		},
	}, log, nil, &configstore.GovernanceConfig{}, gateTestCatalog(t), nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = plugin.Cleanup() })

	client, err := bifrost.Init(t.Context(), schemas.BifrostConfig{
		Account:    gateTestAccount{baseURL: spyURL},
		Logger:     log,
		LLMPlugins: []schemas.LLMPlugin{plugin},
	})
	require.NoError(t, err)
	t.Cleanup(func() { client.Shutdown() })

	config := &lib.Config{
		ClientConfig:    &configstore.ClientConfig{EnforceAuthOnInference: false},
		WebSocketConfig: &schemas.WebSocketConfig{MaxConnections: 8},
	}
	pool := bfws.NewPool(&schemas.WSPoolConfig{})
	t.Cleanup(pool.Close)
	handler := NewWSRealtimeHandler(client, config, pool)
	t.Cleanup(handler.Close)

	r := router.New()
	handler.RegisterRoutes(r)
	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	srv := &fasthttp.Server{Handler: r.Handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Shutdown()
	})

	return &gateTestGateway{client: client, spy: spy, door: door, base: "ws://" + ln.Addr().String()}
}

// gateTestCatalog prices the model the tests address as PAID, so the gate's pricing branch
// sends its input to the unit/door business instead of the explicitly-free bypass.
func gateTestCatalog(t *testing.T) *modelcatalog.ModelCatalog {
	t.Helper()
	ds := datasheet.NewTestStore(nil)
	paid := 0.01
	ds.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: "gpt-4o-realtime-preview", Provider: string(schemas.OpenAI), Mode: "chat", InputCostPerToken: &paid},
	})
	return modelcatalog.NewTestCatalogWithDatasheet(ds)
}

// connect dials one realtime session through the real gateway and waits until the gateway's
// upstream leg has reached the provider spy, so a later "nothing arrived" assertion is
// about the framed input rather than about a connection that never opened.
func (g *gateTestGateway) connect(t *testing.T) *ws.Conn {
	t.Helper()
	conn, resp, err := ws.DefaultDialer.Dial(g.base+"/openai/v1/realtime?model=gpt-4o-realtime-preview", nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })
	if resp != nil {
		assert.Equal(t, fasthttp.StatusSwitchingProtocols, resp.StatusCode)
	}
	select {
	case <-g.spy.conns:
	case <-time.After(10 * time.Second):
		t.Fatal("the gateway never opened the upstream provider connection")
	}
	return conn
}

// assertNothingReachedTheProvider proves the spy received no frame for a bounded window.
func (g *gateTestGateway) assertNothingReachedTheProvider(t *testing.T) {
	t.Helper()
	select {
	case frame := <-g.spy.frames:
		t.Fatalf("the provider received %q, but the input is not covered by a reserved unit", string(frame))
	case <-time.After(750 * time.Millisecond):
	}
}

// readClientError reads the next error frame the gateway wrote back to the client.
func readClientError(t *testing.T, conn *ws.Conn) map[string]any {
	t.Helper()
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(10*time.Second)))
	for {
		_, data, err := conn.ReadMessage()
		require.NoError(t, err, "the gateway must answer the intercepted input in band")
		var frame map[string]any
		require.NoError(t, json.Unmarshal(data, &frame), "realtime frames are JSON: %s", string(data))
		if frame["type"] == string(schemas.RTEventError) {
			return frame
		}
	}
}

func TestWSRealtimeUnreservedAudioInputIsNotForwardedToTheProvider(t *testing.T) {
	gateway := newGateTestGateway(t, nil, nil) // admission on, no reserve door: nothing can be reserved
	conn := gateway.connect(t)

	// The browser appends billable audio. No unit was ever reserved for this session, so the
	// gate must stop it before the upstream write.
	require.NoError(t, conn.WriteMessage(ws.TextMessage, []byte(`{"type":"input_audio_buffer.append","audio":"aGVsbG8="}`)))

	// The ticket's own assertion: the provider spy stays silent.
	gateway.assertNothingReachedTheProvider(t)

	errFrame := readClientError(t, conn)
	errBody, ok := errFrame["error"].(map[string]any)
	require.True(t, ok, "the error frame carries an error object: %v", errFrame)
	message, _ := errBody["message"].(string)
	assert.Contains(t, message, "not covered by a reserved billing unit",
		"the client is told why its audio was not forwarded")
	assert.Contains(t, message, governance.RealtimeInputInterceptUnitNotReserved,
		"the recorded interception reason rides the client-facing answer")
}

func TestWSRealtimeClientCannotBypassTheGateWithEventParameters(t *testing.T) {
	gateway := newGateTestGateway(t, nil, nil)
	conn := gateway.connect(t)

	// Every field a client could invent to look reserved, on the wire where the gate sees it.
	// The decision reads the session's unit registry and the reserve door only, so none of it
	// changes the verdict.
	adversarial := `{"type":"input_audio_buffer.append","audio":"aGVsbG8=",` +
		`"reservation_id":"cr-forged","grant_id":"grant-forged","free":true,` +
		`"payer":{"kind":"personal","id":"someone-else"}}`
	require.NoError(t, conn.WriteMessage(ws.TextMessage, []byte(adversarial)))

	// The ticket's own assertion: the provider spy stays silent.
	gateway.assertNothingReachedTheProvider(t)

	errFrame := readClientError(t, conn)
	errBody, ok := errFrame["error"].(map[string]any)
	require.True(t, ok, "the error frame carries an error object: %v", errFrame)
	message, _ := errBody["message"].(string)
	assert.True(t, strings.Contains(message, governance.RealtimeInputInterceptUnitNotReserved),
		"event fields cannot make an ungated unit look reserved: %q", message)
}

func TestWSRealtimeReservedUnitAudioStillReachesTheProvider(t *testing.T) {
	// The positive control (AC4): with a reserve door that HOLDS a unit for this session's
	// first billed input, the very same append must reach the provider — the gate tightens
	// nothing for reserved units, and it does not eat the frame it allowed.
	door := newGateTestReserveDoor(t)
	ceiling := int64(8192)
	reserve := &governance.CreditReserveConfig{
		Endpoint:           door.endpoint,
		ServiceSecret:      "gate-test-secret",
		GrantID:            "grant-ws-gate-1",
		PayerKind:          "personal",
		PayerID:            "user-ws-gate-1",
		SnapshotVersion:    7,
		RateVersion:        3,
		InputCeilingTokens: ceiling,
		MaximumCredits:     100,
		TimeoutMs:          5000,
	}
	gateway := newGateTestGateway(t, reserve, door)
	conn := gateway.connect(t)

	require.NoError(t, conn.WriteMessage(ws.TextMessage, []byte(`{"type":"input_audio_buffer.append","audio":"aGVsbG8="}`)))

	select {
	case frame := <-gateway.spy.frames:
		// The append forwarded, translated to the provider shape.
		assert.Contains(t, string(frame), "input_audio_buffer.append",
			"the provider receives the billed audio over the held unit")
	case <-time.After(10 * time.Second):
		t.Fatal("the append over a held unit never reached the provider")
	}
	assert.EqualValues(t, 1, door.calls.Load(), "the gate opened exactly one unit before the forward")
}
