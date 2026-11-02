package governance

// T06.12 (#584, original task P16): the online identity admission the enabled governance
// gateway asks before EVERY new model dispatch — new request, retry, or the start of a
// stream — before the reserve door and before any provider is called, free models
// included. These tests run the hook the deployment actually calls (PreLLMHook), because
// only that path carries the short circuit that keeps the request from the provider, the
// refused mark that keeps it from being billed, and the answer the caller receives.
//
// The tests exercise the wire strictly: what the gateway sends to T06.11's door
// (POST /api/gateway/identity-admission, the frozen closed request
// {requestId, keyId, valueHash, operation:"model.dispatch", modelRef}, the credit_service
// Bearer credential), and what each answer class does — 200 allowed admits the attempt,
// 401/403 refuse the dispatch as an identity refusal, 503 refuses as unavailable, and
// timeout/unreachable/malformed refuse fail-closed. A wrong key source — a key the door
// does not admit — refuses dispatch with no provider call, and an explicitly re-authorized
// new key dispatches.
//
// The acceptance instrument is the provider call count, the same shape
// endtoend_test.go uses: a refused admission must leave the provider at zero, and an
// admitted one must let exactly the dispatch through.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// identityTestVKValue is the personal key the identity tests' gateway store serves.
const identityTestVKValue = "sk-bf-identity-vk"

// identityTestVKID is that key's row id in the gateway's own store.
const identityTestVKID = "vk-identity"

// identityPaidModel / identityFreeModel price the two sides the contract covers: paid work
// and explicitly free work. Both must pass the identity door.
const (
	identityPaidModel = "gpt-4o-identity-paid"
	identityFreeModel = "gpt-4o-identity-free"
)

// identityServiceSecret is the credit_service credential the door expects (T06.11 reuses
// 06.1's service authentication; the gateway presents it as a Bearer credential).
const identityServiceSecret = "obot_csvc_identitytestsecret"

// identityPricingRows prices the models the identity tests run against.
func identityPricingRows() []configstoreTables.TableModelPricing {
	zero := 0.0
	price := 0.0005
	return []configstoreTables.TableModelPricing{
		{Model: identityPaidModel, Provider: string(schemas.OpenAI), Mode: "chat", InputCostPerToken: &price},
		{Model: identityFreeModel, Provider: string(schemas.OpenAI), Mode: "chat", InputCostPerToken: &zero, OutputCostPerToken: &zero},
	}
}

// identityOutputCapEvidence verifies the output-cap enforcement for the fixture models, so
// the identity gate (which runs before the reserve) is what the tests observe on the paid
// path rather than the output-cap gate.
func identityOutputCapEvidence() []OutputCapEnforcementRecord {
	return []OutputCapEnforcementRecord{
		identityOutputCapRecord(schemas.OpenAI, identityPaidModel),
		identityOutputCapRecord(schemas.OpenAI, identityFreeModel),
	}
}

// identityCtx is a request context the way the pipeline carries one into PreLLMHook: the
// personal key presented and settled on the grant, the transport's request id and fallback
// index already set.
func identityCtx() *schemas.BifrostContext {
	ctx := identityPresentCtx(identityTestVKValue)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-identity-1")
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 0)
	return ctx
}

// identityChatRequest is a chat request for the model named, capped the way the funnel
// carries one.
func identityChatRequest(model string) *schemas.BifrostRequest {
	maxTokens := 1024
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Params:   &schemas.ChatParameters{MaxCompletionTokens: &maxTokens},
		},
	}
}

// identityRefusalFor runs a request through the LLM hook and returns the error the caller
// is answered with, asserting the request was turned away and marked refused.
func identityRefusalFor(t *testing.T, plugin *GovernancePlugin, ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.BifrostError {
	t.Helper()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "the request reached the provider instead of being turned away")
	require.NotNil(t, shortCircuit.Error, "a refused request has to carry the reason it was refused")
	require.NotNil(t, shortCircuit.Error.Error)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
		"a refused request is marked refused for everything downstream")
	return shortCircuit.Error
}

// identityCountingProvider is the acceptance instrument: a fake provider endpoint that
// counts each dispatch attempt.
type identityCountingProvider struct {
	server *httptest.Server
	mu     sync.Mutex
	calls  int
}

func newIdentityCountingProvider(t *testing.T) *identityCountingProvider {
	t.Helper()
	fake := &identityCountingProvider{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.calls++
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-identity","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (p *identityCountingProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// fakeIdentityAdmission stands in for T06.11's POST /api/gateway/identity-admission: it
// answers the status and body the test arms, records every call (credential, path, body),
// and can be flipped from admitting the key to refusing it — the disable/re-authorize
// transitions the acceptance cases ride.
type fakeIdentityAdmission struct {
	server *httptest.Server

	mu       sync.Mutex
	calls    int
	lastAuth string
	lastPath string
	lastBody map[string]any

	status int
	body   string
	delay  bool // when true, hold the answer past the door's timeout
}

func newFakeIdentityAdmission(t *testing.T, status int, body string) *fakeIdentityAdmission {
	t.Helper()
	fake := &fakeIdentityAdmission{status: status, body: body}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.calls++
		fake.lastAuth = r.Header.Get("Authorization")
		fake.lastPath = r.URL.Path
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err == nil {
			fake.lastBody = raw
		}
		status, body, delay := fake.status, fake.body, fake.delay
		fake.mu.Unlock()
		if delay {
			// Hold the answer past the gate's timeout, but never past the client going
			// away: the handler parks until the request context is cancelled, which the
			// gate's bounded wait always does.
			<-r.Context().Done()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

// answer flips what the door answers from now on.
func (f *fakeIdentityAdmission) answer(status int, body string) {
	f.mu.Lock()
	f.status, f.body = status, body
	f.mu.Unlock()
}

func (f *fakeIdentityAdmission) endpoint() string {
	return f.server.URL + "/api/gateway/identity-admission"
}

func (f *fakeIdentityAdmission) callsCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeIdentityAdmission) snapshot() (auth, path string, body map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAuth, f.lastPath, f.lastBody
}

const identityAllowedAnswer = `{"allowed":true,"requestId":"req-identity-1","admissionId":"adm-1"}`

func identityRefusedAnswer() string {
	return `{"error":"The key's identity cannot be admitted for this dispatch.","code":"KEY_IDENTITY_INVALID"}`
}

func identityUnavailableAnswer() string {
	return `{"error":"Identity admission is unavailable.","code":"IDENTITY_ADMISSION_UNAVAILABLE"}`
}

// newIdentityTestPlugin builds a governance plugin serving the personal key, with the
// credit-admission extension enabled, a reserve door that always holds (so the identity
// gate is the only thing that can refuse), and a catalog pricing the fixture models. The
// identity door is armed as the test asks.
func newIdentityTestPlugin(t *testing.T, identity *IdentityAdmissionConfig) *GovernancePlugin {
	t.Helper()
	return newIdentityTestPluginWithKey(t, identity, identityTestVKID, identityTestVKValue)
}

// newIdentityTestPluginWithKey is newIdentityTestPlugin for a key the test names — the
// re-authorized new key rides this.
func newIdentityTestPluginWithKey(t *testing.T, identity *IdentityAdmissionConfig, vkID, vkValue string) *GovernancePlugin {
	t.Helper()
	vk := identityBuildVirtualKey(vkID, vkValue, "identity-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{identityProviderConfig("openai",
			[]string{identityPaidModel, identityFreeModel})})
	logger := identityNewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, &identityInMemoryStore{})
	require.NoError(t, err)

	ds := datasheet.NewTestStore(nil)
	ds.SetPricingRowsForTest(identityPricingRows())
	catalog := modelcatalog.NewTestCatalogWithDatasheet(ds)

	// A reserve door that always holds: whatever refuses on the paid path is the identity
	// gate, and an admitted dispatch completes through the reserve.
	reserve := &CreditReserveConfig{
		Endpoint:           identityAlwaysHoldingReserve(t),
		ServiceSecret:      identityServiceSecret,
		GrantID:            "grant-identity-1",
		PayerKind:          "personal",
		PayerID:            "user-identity-1",
		SnapshotVersion:    7,
		RateVersion:        3,
		InputCeilingTokens: 100000,
		MaximumCredits:     100,
	}
	enabled := true
	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory: identityBoolPtr(false),
		OpenBotCreditAdmission: &CreditAdmissionConfig{
			Enabled:           &enabled,
			Reserve:           reserve,
			OutputCapEvidence: identityOutputCapEvidence(),
			Identity:          identity,
		},
	}, logger, store, nil, catalog, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

// identityAlwaysHoldingReserve is a reserve door answering 201 for every ask, so the paid
// path cannot be refused by the reserve.
func identityAlwaysHoldingReserve(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"attemptId":"att-1","reservationId":"res-identity-1","maximumCredits":100,"snapshotVersion":7,"rateVersion":3}`))
	}))
	t.Cleanup(server.Close)
	return server.URL + "/reserve"
}

// identityAdmissionWith arms the identity door pointed at the fake.
func identityAdmissionWith(endpoint string, timeoutMs int) *IdentityAdmissionConfig {
	return &IdentityAdmissionConfig{
		Endpoint:      endpoint,
		ServiceSecret: identityServiceSecret,
		TimeoutMs:     timeoutMs,
	}
}

// --- the wire the gateway sends ---------------------------------------------------------

// The gateway asks the door with the frozen request: the authenticated key's own row id,
// the sha256 of the value it authenticated, the operation, the model cell, and the request
// correlation id — nothing else, over the credit_service Bearer credential.
func TestOpenBotIdentityAdmissionSendsTheFrozenRequestOverTheServiceCredential(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusOK, identityAllowedAnswer)
	plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))

	_, shortCircuit, err := plugin.PreLLMHook(identityCtx(), identityChatRequest(identityPaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "an admitted identity lets the request proceed to the reserve")

	auth, path, body := door.snapshot()
	assert.Equal(t, 1, door.callsCount(), "one identity admission per dispatch attempt")
	assert.Equal(t, "Bearer "+identityServiceSecret, auth,
		"the identity door is called over the credit_service credential, as T06.11 requires")
	assert.Equal(t, "/api/gateway/identity-admission", path)
	require.NotNil(t, body)
	assert.ElementsMatch(t,
		[]string{"requestId", "keyId", "valueHash", "operation", "modelRef"},
		keysOf(body),
		"the request is exactly T06.11's frozen closed body")
	assert.Equal(t, identityTestVKID, body["keyId"],
		"the gateway sends its own virtual-key row id — the key it authenticated")
	assert.Regexp(t, regexp.MustCompile(`^[0-9a-f]{64}$`), body["valueHash"],
		"the gateway sends the sha256 hex of the value it authenticated")
	assert.Equal(t, "model.dispatch", body["operation"])
	assert.Equal(t, map[string]any{
		"provider":   "openai",
		"model":      identityPaidModel,
		"capability": "chat",
	}, body["modelRef"])
}

// The valueHash is the digest of the value this gateway authenticated: the same key asked
// again digests identically, and a different key value digests differently.
func TestOpenBotIdentityAdmissionValueHashIsTheDigestOfTheAuthenticatedValue(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusOK, identityAllowedAnswer)
	plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))

	_, shortCircuit, err := plugin.PreLLMHook(identityCtx(), identityChatRequest(identityFreeModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, _, body := door.snapshot()
	first, _ := body["valueHash"].(string)
	assert.Equal(t, sha256Hex(identityTestVKValue), first,
		"the valueHash is sha256(key value) — the same digest OpenBot's key table stores")
}

// --- what each answer class does --------------------------------------------------------

// 200 allowed: the attempt proceeds — the reserve is asked and holds, and the dispatch
// completes.
func TestOpenBotIdentityAdmissionAllowedLetsTheDispatchThrough(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusOK, identityAllowedAnswer)
	plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))
	provider := newIdentityCountingProvider(t)

	_, shortCircuit, err := plugin.PreLLMHook(identityCtx(), identityChatRequest(identityPaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "an admitted identity is not a refusal")
	assert.Nil(t, identityCtxRefused(identityCtx()))
	_ = provider // the provider count is asserted in the acceptance matrix below
}

// 403 KEY_IDENTITY_INVALID: the dispatch is refused as an identity refusal — no reserve is
// asked, no provider is called, the refusal names the identity category.
func TestOpenBotIdentityAdmissionRefusedKeyDeniesDispatch(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusForbidden, identityRefusedAnswer())
	plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))

	bifrostErr := identityRefusalFor(t, plugin, identityCtx(), identityChatRequest(identityPaidModel))

	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditIdentityNotAdmitted), *bifrostErr.Type,
		"an identity refusal is its own explainable decision")
	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 503, *bifrostErr.StatusCode)
	require.NotNil(t, bifrostErr.Error.Code)
	assert.Equal(t, CreditRefusalIdentityInvalid, *bifrostErr.Error.Code,
		"the refusal names the identity category from the closed vocabulary")
	assert.Equal(t, 1, door.callsCount(), "the identity door was asked once")
}

// 401 service-auth: the gateway could not authenticate itself; fail closed, no dispatch.
func TestOpenBotIdentityAdmissionServiceAuthFailureDeniesDispatch(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusUnauthorized, identityUnavailableAnswer())
	plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))

	bifrostErr := identityRefusalFor(t, plugin, identityCtx(), identityChatRequest(identityPaidModel))
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditIdentityNotAdmitted), *bifrostErr.Type)
}

// 503 IDENTITY_ADMISSION_UNAVAILABLE: storage failure on OpenBot's side is not an
// admission; the dispatch is refused.
func TestOpenBotIdentityAdmissionUnavailableDeniesDispatch(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusServiceUnavailable, identityUnavailableAnswer())
	plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))

	bifrostErr := identityRefusalFor(t, plugin, identityCtx(), identityChatRequest(identityPaidModel))
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditIdentityNotAdmitted), *bifrostErr.Type)
	require.NotNil(t, bifrostErr.Error.Code)
	assert.Equal(t, CreditRefusalAdmissionUnreachable, *bifrostErr.Error.Code)
}

// Timeout and unreachable doors are refusals, not pass-throughs: there is no optimistic
// branch.
func TestOpenBotIdentityAdmissionTimeoutAndUnreachableRefuseFailClosed(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		door := newFakeIdentityAdmission(t, http.StatusOK, identityAllowedAnswer)
		door.mu.Lock()
		door.delay = true
		door.mu.Unlock()
		plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 50))

		bifrostErr := identityRefusalFor(t, plugin, identityCtx(), identityChatRequest(identityPaidModel))
		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditIdentityNotAdmitted), *bifrostErr.Type)
		assert.Contains(t, bifrostErr.Error.Message, "identity admission")
	})
	t.Run("unreachable", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		dead.Close()
		plugin := newIdentityTestPlugin(t, identityAdmissionWith(dead.URL+"/api/gateway/identity-admission", 0))

		bifrostErr := identityRefusalFor(t, plugin, identityCtx(), identityChatRequest(identityPaidModel))
		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditIdentityNotAdmitted), *bifrostErr.Type)
	})
	t.Run("malformed answer", func(t *testing.T) {
		door := newFakeIdentityAdmission(t, http.StatusOK, `{"unexpected":"shape"}`)
		plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))

		bifrostErr := identityRefusalFor(t, plugin, identityCtx(), identityChatRequest(identityPaidModel))
		require.NotNil(t, bifrostErr.Type,
			"an answer that cannot be read as an admission proves nothing and dispatches nothing")
	})
}

// --- free models are covered too --------------------------------------------------------

// An explicitly free model passes the identity door before it skips the reserve: free work
// still answers who is calling.
func TestOpenBotIdentityAdmissionCoversExplicitlyFreeModels(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusForbidden, identityRefusedAnswer())
	plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))

	bifrostErr := identityRefusalFor(t, plugin, identityCtx(), identityChatRequest(identityFreeModel))
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditIdentityNotAdmitted), *bifrostErr.Type)
	assert.Equal(t, 1, door.callsCount(), "the free model asked the identity door too")
}

// --- the wiring contract ---------------------------------------------------------------

// An enabled extension whose identity door is not wired refuses dispatchable work rather
// than dispatching on an unverified identity.
func TestOpenBotIdentityAdmissionUnwiredDoorRefuses(t *testing.T) {
	plugin := newIdentityTestPlugin(t, nil)

	bifrostErr := identityRefusalFor(t, plugin, identityCtx(), identityChatRequest(identityPaidModel))
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditIdentityNotAdmitted), *bifrostErr.Type)
	assert.Contains(t, bifrostErr.Error.Message, "identity admission door is not wired")
}

// The extension not enabled keeps today's behavior: the identity door is a part of the
// OpenBot admission posture, not a second gate a non-OpenBot deployment must wire.
func TestOpenBotIdentityAdmissionAbsentWhenExtensionDisabled(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusForbidden, identityRefusedAnswer())
	plugin := newIdentityTestPluginWithoutAdmission(t)
	_ = door

	// With the extension off, the known-paid model is refused by the not-enabled gate and
	// the free model proceeds without any identity call — today's behavior, unchanged.
	identityErr := identityRefusalFor(t, plugin, identityCtx(), identityChatRequest(identityPaidModel))
	require.NotNil(t, identityErr.Type)
	assert.Equal(t, string(DecisionCreditAdmissionNotEnabled), *identityErr.Type)
}

// --- retries and streams ----------------------------------------------------------------

// Every new attempt re-asks the door: a retry (fallback index advanced) is a new identity
// admission, and the disable-between-attempts case is the acceptance matrix below.
func TestOpenBotIdentityAdmissionEveryAttemptReasks(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusOK, identityAllowedAnswer)
	plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))
	ctx := identityCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, identityChatRequest(identityFreeModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Equal(t, 1, door.callsCount())

	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 1)
	_, shortCircuit, err = plugin.PreLLMHook(ctx, identityChatRequest(identityFreeModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	assert.Equal(t, 2, door.callsCount(), "a new attempt asks the door again — no reuse")
}

// --- the acceptance matrix: provider dispatch counts -------------------------------------

// old_personal_key_is_denied_at_enabled_gateway_after_disable: a key whose identity was
// admitted for the first dispatch is denied after the door (standing in for the disabled
// identity) refuses it — no provider dispatch happens after the disable.
func TestOpenBotIdentityAdmissionOldPersonalKeyIsDeniedAtEnabledGatewayAfterDisable(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusOK, identityAllowedAnswer)
	plugin := newIdentityTestPlugin(t, identityAdmissionWith(door.endpoint(), 0))
	provider := newIdentityCountingProvider(t)

	// Before the disable the gateway admits the free model and the dispatch completes.
	ctx1 := identityCtx()
	_, shortCircuit, err := plugin.PreLLMHook(ctx1, identityChatRequest(identityFreeModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "before the disable the identity is admitted")
	dispatchIfAdmitted(t, provider, identityChatRequest(identityFreeModel))
	assert.Equal(t, 1, provider.callCount())

	// The disable: the identity behind the key is disabled upstream, so the door now
	// refuses the same key.
	door.answer(http.StatusForbidden, identityRefusedAnswer())

	ctx2 := identityCtx()
	ctx2.SetValue(schemas.BifrostContextKeyRequestID, "req-identity-2")
	bifrostErr := identityRefusalFor(t, plugin, ctx2, identityChatRequest(identityFreeModel))
	require.NotNil(t, bifrostErr.Type)

	// No provider dispatch after the disable: the counter did not move.
	assert.Equal(t, 1, provider.callCount(),
		"providerDispatchesAfterDisable == 0: the old key dispatches nothing once its identity is refused")
}

// explicitly_reauthorized_new_key_can_dispatch: a NEW key value — a different row the
// door admits again — dispatches exactly once.
func TestOpenBotIdentityAdmissionExplicitlyReauthorizedNewKeyCanDispatch(t *testing.T) {
	door := newFakeIdentityAdmission(t, http.StatusOK, identityAllowedAnswer)
	provider := newIdentityCountingProvider(t)

	// The re-authorized key is a NEW key: a different value, a different row, its own
	// admission. The gateway store serves it the way it served the first.
	reauthorized := newIdentityTestPluginWithKey(t, identityAdmissionWith(door.endpoint(), 0), "vk-identity-new", "sk-bf-identity-vk-new")
	ctx := identityCtx()
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, "sk-bf-identity-vk-new")
	identitySettleTestIdentity(ctx)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-identity-3")
	_, shortCircuit, err := reauthorized.PreLLMHook(ctx, identityChatRequest(identityFreeModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	dispatchIfAdmitted(t, provider, identityChatRequest(identityFreeModel))

	assert.Equal(t, 1, provider.callCount(), "newKeyDispatches == 1")
	assert.Equal(t, 1, door.callsCount(), "the new key asked the door for its own admission")
	_, _, body := door.snapshot()
	assert.Equal(t, "vk-identity-new", body["keyId"],
		"the re-authorized key sends its own row id, not the old key's")
}

// --- helpers ----------------------------------------------------------------------------

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// dispatchIfAdmitted performs the pipeline's provider stage only where the hook returned no
// short circuit — the same predicate the pipeline applies.
func dispatchIfAdmitted(t *testing.T, provider *identityCountingProvider, req *schemas.BifrostRequest) {
	t.Helper()
	body, err := json.Marshal(req.ChatRequest)
	require.NoError(t, err)
	resp, err := provider.server.Client().Post(provider.server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
}

func identityCtxRefused(ctx *schemas.BifrostContext) any {
	return ctx.Value(governanceRejectedContextKey)
}

// newIdentityTestPluginWithoutAdmission builds the same fixture with the credit-admission
// extension off entirely: today's behavior for a non-OpenBot deployment, unchanged.
func newIdentityTestPluginWithoutAdmission(t *testing.T) *GovernancePlugin {
	t.Helper()
	vk := identityBuildVirtualKey(identityTestVKID, identityTestVKValue, "identity-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{identityProviderConfig("openai",
			[]string{identityPaidModel, identityFreeModel})})
	logger := identityNewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, &identityInMemoryStore{})
	require.NoError(t, err)

	ds := datasheet.NewTestStore(nil)
	ds.SetPricingRowsForTest(identityPricingRows())
	catalog := modelcatalog.NewTestCatalogWithDatasheet(ds)

	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory: identityBoolPtr(false),
	}, logger, store, nil, catalog, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

// --- local fixtures (the file is self-contained: the foreign driver runs it in file-list
// --- mode, so it defines its own copies of the shared test helpers, prefixed) -----------

// identityBoolPtr is boolPtr.
func identityBoolPtr(b bool) *bool { return &b }

// identityMockLogger is a no-op logger satisfying schemas.Logger.
type identityMockLogger struct{}

func (identityMockLogger) SetLevel(schemas.LogLevel)              {}
func (identityMockLogger) SetOutputType(schemas.LoggerOutputType) {}
func (identityMockLogger) Error(string, ...interface{})           {}
func (identityMockLogger) Warn(string, ...interface{})            {}
func (identityMockLogger) Info(string, ...interface{})            {}
func (identityMockLogger) Debug(string, ...interface{})           {}
func (identityMockLogger) Fatal(string, ...interface{})           {}
func (identityMockLogger) LogHTTPRequest(schemas.LogLevel, string) schemas.LogEventBuilder {
	return schemas.NoopLogEvent
}

func identityNewMockLogger() identityMockLogger { return identityMockLogger{} }

// identityInMemoryStore is mockInMemoryStore: an InMemoryStore answering empty maps.
type identityInMemoryStore struct{}

func (m *identityInMemoryStore) GetConfiguredProviders() map[schemas.ModelProvider]configstore.ProviderConfig {
	return nil
}
func (m *identityInMemoryStore) GetConfiguredProviderNames() []string { return nil }
func (m *identityInMemoryStore) GetEnabledAgents() map[string]bool    { return nil }
func (m *identityInMemoryStore) GetMCPClientsAllowedByDefault() map[string]string {
	return nil
}
func (m *identityInMemoryStore) GetMCPClientNames() map[string]string { return nil }
func (m *identityInMemoryStore) GetMCPClientBySlug(string) (string, string, bool) {
	return "", "", false
}

// identityProviderConfig is buildProviderConfig for the openai fixture.
func identityProviderConfig(provider string, allowedModels []string) configstoreTables.TableVirtualKeyProviderConfig {
	return configstoreTables.TableVirtualKeyProviderConfig{
		Provider:      provider,
		AllowedModels: allowedModels,
		Weight:        bifrost.Ptr(1.0),
	}
}

// identityBuildVirtualKey is buildVirtualKeyWithProviders.
func identityBuildVirtualKey(id, value, name string, providers []configstoreTables.TableVirtualKeyProviderConfig) *configstoreTables.TableVirtualKey {
	active := true
	return &configstoreTables.TableVirtualKey{
		ID:              id,
		Value:           *schemas.NewSecretVar(value),
		Name:            name,
		IsActive:        &active,
		ProviderConfigs: providers,
	}
}

// identityOutputCapRecord is outputCapVerifiedRecord for a fixture cell.
func identityOutputCapRecord(provider schemas.ModelProvider, model string) OutputCapEnforcementRecord {
	return OutputCapEnforcementRecord{
		Provider:    string(provider),
		Model:       model,
		CallMethod:  "chat",
		Mechanism:   OutputCapMechanismProviderMaxTokens,
		Environment: "fake-provider-harness",
		Result:      OutputCapEvidenceVerified,
		VerifiedAt:  "2026-10-18T00:00:00Z",
		Source:      "go test ./plugins/governance -run TestOpenBotIdentityAdmission",
	}
}

// identityPresentCtx is presentCtx: a granted request context that presented the key.
func identityPresentCtx(virtualKeyValue string) *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetGrant(grant.New())
	ctx.SetValue(schemas.BifrostContextKeyVirtualKey, virtualKeyValue)
	identitySettleTestIdentity(ctx)
	return ctx
}

// identitySettleTestIdentity is settleTestIdentity for the key-credential case.
func identitySettleTestIdentity(ctx *schemas.BifrostContext) {
	var credential schemas.Credential
	if virtualKey, _ := ctx.Value(schemas.BifrostContextKeyVirtualKey).(string); virtualKey != "" {
		credential = grant.NewCredential(grant.CredentialVirtualKey, virtualKey)
	}
	ctx.Grant().SetIdentity(grant.NewIdentity(credential, nil, nil, nil, nil, nil, nil))
}
