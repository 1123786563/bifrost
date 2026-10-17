package governance

// The PreLLM reserve seam (07.2): with the credit-admission extension enabled, a model that is
// not explicitly free is dispatched only after the deployment's billing service has ATOMICALLY
// held a reservation for it, over the trusted service identity 06.1 issued. The tests below run
// the seam against a fake billing service standing in for OpenBot's credit protocol surface
// (`POST /reserve`, 201 reserved / 422 refused), because the seam's contract is the wire: what
// is sent, what each answer class does, and what the request context carries afterwards.
//
// The three answer classes and the one hard rule:
//   - reserved  -> the request continues, and the context carries the reservationId and the
//     locked versions the answer echoed (06.2's contract; 06.5's version locking lands later,
//     so the echoed fields are the locked basis there is).
//   - refused   -> the same short-circuit refusal path as every other gate answer: the request
//     is marked refused and no provider is called.
//   - timeout / unreachable / anything-not-a-held-reservation -> treated as refused. There is
//     no optimistic pass-through branch; a deployment whose billing service cannot answer does
//     not dispatch paid work (AC05).
//
// The attempt identity is minted on the gateway, deterministically per request attempt, and
// rides the reserve as the idempotency key: a replayed reserve for the same attempt answers the
// original reservation (06.7's same-id recovery), so a lost answer can be queried, never
// re-billed. These tests assert that identity by observing what actually went over the wire.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// reserveTestVKValue is the key value the reserve tests' store serves.
const reserveTestVKValue = "sk-bf-reserve-vk"

// reserveServiceSecret is the trusted service identity the fake expects (06.1's obot_csvc_
// shape; the seam must present it as a Bearer credential and nothing else).
const reserveServiceSecret = "obot_csvc_reservetestsecret"

// The four pricing states the seam distinguishes: paid (reserves), explicitly free (does not
// reserve), negative-rate (the catalog's "unpriced" marker - never free), and no entry at all
// (unknown; goes through the reserve door where OpenBot's price snapshot refuses it).
const (
	reservePaidModel     = "gpt-4o-reserve-paid"
	reserveFreeModel     = "gpt-4o-reserve-free"
	reserveNegativeModel = "gpt-4o-reserve-negative"
	reserveUnknownModel  = "model-reserve-no-entry"
)

// reserveTestPricingRows prices the models the reserve tests run against, one row per state.
func reserveTestPricingRows() []configstoreTables.TableModelPricing {
	zero := 0.0
	price := 0.0005
	negative := -0.5
	return []configstoreTables.TableModelPricing{
		{Model: reservePaidModel, Provider: string(schemas.OpenAI), Mode: "chat", InputCostPerToken: &price},
		{Model: reserveFreeModel, Provider: string(schemas.OpenAI), Mode: "chat", InputCostPerToken: &zero, OutputCostPerToken: &zero},
		{Model: reserveNegativeModel, Provider: string(schemas.OpenAI), Mode: "chat", InputCostPerToken: &negative},
	}
}

// newReserveTestPlugin builds a governance plugin with the admission switch in the state the
// test asks for, a catalog carrying the pricing rows above, and a key allowed all four models.
func newReserveTestPlugin(t *testing.T, admission *CreditAdmissionConfig) *GovernancePlugin {
	t.Helper()
	vk := buildVirtualKeyWithProviders("vk-reserve", reserveTestVKValue, "reserve-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai",
			[]string{reservePaidModel, reserveFreeModel, reserveNegativeModel, reserveUnknownModel})})
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, &mockInMemoryStore{})
	require.NoError(t, err)

	ds := datasheet.NewTestStore(nil)
	ds.SetPricingRowsForTest(reserveTestPricingRows())
	catalog := modelcatalog.NewTestCatalogWithDatasheet(ds)

	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory:          boolPtr(false),
		OpenBotCreditAdmission: admission,
	}, logger, store, nil, catalog, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

// reserveTestCtx is a request context the way the pipeline carries one into PreLLMHook: the
// transport's request id and the attempt's fallback index already settled, so the attempt
// identity the seam derives is the one production would derive.
func reserveTestCtx() *schemas.BifrostContext {
	ctx := presentCtx(reserveTestVKValue)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-reserve-1")
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 0)
	return ctx
}

// reserveChatRequest is a chat request for the model named, the same shape the funnel carries.
func reserveChatRequest(model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: model},
	}
}

// admissionWithReserve arms the extension with a reserve door pointed at the fake.
func admissionWithReserve(endpoint string, timeoutMs int) *CreditAdmissionConfig {
	enabled := true
	return &CreditAdmissionConfig{
		Enabled: &enabled,
		Reserve: &CreditReserveConfig{
			Endpoint:        endpoint,
			ServiceSecret:   reserveServiceSecret,
			GrantID:         "grant-reserve-1",
			PayerKind:       "personal",
			PayerID:         "user-reserve-1",
			SnapshotVersion: 7,
			RateVersion:     3,
			BoundEvidence: []CreditReserveBoundEvidence{
				{Kind: "provider_max_tokens", MaximumTokens: 4096},
			},
			MaximumCredits: 100,
			TimeoutMs:      timeoutMs,
		},
	}
}

// enabledAdmission is the extension enabled with NO reserve door: the seam must refuse known
// dispatchable work rather than pass it unreserved.
func enabledAdmission() *CreditAdmissionConfig {
	enabled := true
	return &CreditAdmissionConfig{Enabled: &enabled}
}

// fakeCreditService stands in for OpenBot's credit protocol surface: one door, POST /reserve,
// answering the status and body the test arms. It records every call - the credential, the
// body - so the tests can assert what the seam actually put on the wire.
type fakeCreditService struct {
	server *httptest.Server

	mu           sync.Mutex
	reserveCount int
	lastAuth     string
	lastBody     map[string]any

	status int
	body   string
	delay  time.Duration
}

func newFakeCreditService(t *testing.T, status int, body string) *fakeCreditService {
	t.Helper()
	fake := &fakeCreditService{status: status, body: body}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.reserveCount++
		fake.lastAuth = r.Header.Get("Authorization")
		var raw map[string]any
		if err := json.NewDecoder(r.Body).Decode(&raw); err == nil {
			fake.lastBody = raw
		}
		status, body, delay := fake.status, fake.body, fake.delay
		fake.mu.Unlock()
		if delay > 0 {
			time.Sleep(delay)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeCreditService) endpoint() string { return f.server.URL + "/reserve" }

func (f *fakeCreditService) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reserveCount
}

func (f *fakeCreditService) snapshot() (auth string, body map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAuth, f.lastBody
}

// reservedAnswer is the 201 a held reservation carries (06.2's response contract): the trusted
// attempt id OpenBot minted (att- + its reservation id, 06.7), the reservation id, and the
// locked versions echoed back.
func reservedAnswer() string {
	return `{"attemptId":"att-res-1","reservationId":"res-reserve-1","maximumCredits":100,"snapshotVersion":7,"rateVersion":3}`
}

// refusedAnswer is the 422 a refusal carries: a closed reason code from OpenBot's vocabulary.
func refusedAnswer(code string) string {
	return `{"error":"refused","code":"` + code + `","detail":{}}`
}

// reserveRefusalFor runs a request through the LLM hook and returns the error the caller is
// answered with, asserting the request was turned away and marked like every other refusal.
func reserveRefusalFor(t *testing.T, plugin *GovernancePlugin, ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.BifrostError {
	t.Helper()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "the request reached the provider instead of being turned away")
	require.NotNil(t, shortCircuit.Error, "a refused request has to carry the reason it was refused")
	require.NotNil(t, shortCircuit.Error.Error)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
		"a request without a held reservation is marked refused for everything downstream")
	return shortCircuit.Error
}

// --- the seam's wire contract -----------------------------------------------------------

func TestPreLLMReserveSuccessContinuesTheRequestAndCarriesTheReservation(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))

	require.NoError(t, err)
	require.Nil(t, shortCircuit, "a held reservation lets the request proceed to dispatch")
	assert.Nil(t, ctx.Value(governanceRejectedContextKey))

	// The context carries what PostLLM settlement will settle against: the reservation, the
	// gateway attempt identity, and the locked versions the answer echoed.
	reservation, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok, "a held reservation is stamped on the request context")
	assert.Equal(t, "res-reserve-1", reservation.ReservationID)
	assert.Equal(t, 7, reservation.SnapshotVersion)
	assert.Equal(t, 3, reservation.RateVersion)
	assert.Equal(t, int64(100), reservation.MaximumCredits)
	assert.Regexp(t, regexp.MustCompile(`^bfat-[0-9a-f]{32}$`), reservation.AttemptID,
		"the gateway mints a recognisable, bounded attempt identity")

	// What went over the wire: the trusted service identity, and a request shaped like 06.2's.
	auth, body := fake.snapshot()
	assert.Equal(t, 1, fake.calls(), "one reserve per dispatch")
	assert.Equal(t, "Bearer "+reserveServiceSecret, auth, "the reserve authenticates as the trusted billing identity (06.1)")
	require.NotNil(t, body)
	assert.Equal(t, "grant-reserve-1", body["grantId"])
	assert.Equal(t, map[string]any{"kind": "personal", "id": "user-reserve-1"}, body["payer"])
	assert.Equal(t, map[string]any{"provider": "openai", "model": reservePaidModel, "capability": "chat"}, body["model"])
	assert.InDelta(t, 7.0, body["snapshotVersion"], 0)
	assert.InDelta(t, 3.0, body["rateVersion"], 0)
	assert.InDelta(t, 100.0, body["maximumCredits"], 0)
	assert.Equal(t, []any{map[string]any{"kind": "provider_max_tokens", "maximumTokens": 4096.0}}, body["boundEvidence"])
	assert.Equal(t, reservation.AttemptID, body["idempotencyKey"],
		"the reserve carries the gateway attempt identity as the idempotency key OpenBot recognises")
}

func TestPreLLMReserveRefusalCallsNoProvider(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("insufficient_credits"))
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, plugin, ctx, reserveChatRequest(reservePaidModel))

	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 503, *bifrostErr.StatusCode)
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type,
		"a refused reserve is its own explainable decision, not a generic error")
	assert.Contains(t, bifrostErr.Error.Message, "insufficient_credits",
		"the refusal names the reason code OpenBot answered with")
	assert.Equal(t, 1, fake.calls(), "the refusal came from the reserve door")
	// No provider is called: the short circuit above is the pipeline's only exit, and the
	// refused mark on the context keeps any downstream accounting from billing the work.
}

func TestPreLLMReserveTimeoutIsARefusalNotAPassThrough(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	fake.delay = 300 * time.Millisecond // the answer lands after the seam has stopped waiting
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 50))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, plugin, ctx, reserveChatRequest(reservePaidModel))

	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
	assert.Contains(t, bifrostErr.Error.Message, "timed out",
		"the refusal says the reserve was unanswered, not that the caller failed")
}

func TestPreLLMReserveUnreachableAndServerErrorAreRefusals(t *testing.T) {
	t.Run("endpoint unreachable", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		dead.Close() // the URL now answers nothing
		plugin := newReserveTestPlugin(t, admissionWithReserve(dead.URL+"/reserve", 0))
		ctx := reserveTestCtx()

		bifrostErr := reserveRefusalFor(t, plugin, ctx, reserveChatRequest(reservePaidModel))

		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
		assert.Contains(t, bifrostErr.Error.Message, "unreachable")
	})
	t.Run("server error", func(t *testing.T) {
		fake := newFakeCreditService(t, http.StatusInternalServerError, `{"error":"boom"}`)
		plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
		ctx := reserveTestCtx()

		bifrostErr := reserveRefusalFor(t, plugin, ctx, reserveChatRequest(reservePaidModel))

		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
		assert.Contains(t, bifrostErr.Error.Message, "500")
	})
	t.Run("a 201 that does not parse as a reservation", func(t *testing.T) {
		fake := newFakeCreditService(t, http.StatusCreated, `{"unexpected":"shape"}`)
		plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
		ctx := reserveTestCtx()

		bifrostErr := reserveRefusalFor(t, plugin, ctx, reserveChatRequest(reservePaidModel))

		require.NotNil(t, bifrostErr.Type,
			"an answer that cannot be read as a held reservation proves nothing and dispatches nothing")
		assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
	})
}

// The attempt identity is stable per attempt: re-running the hook for the same request attempt
// reserves under the SAME idempotency key - the property a lost answer's recovery needs (06.7) -
// while a new attempt of the same request mints a new one.
func TestPreLLMReserveAttemptIDIsStablePerAttempt(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, firstBody := fake.snapshot()
	firstKey := firstBody["idempotencyKey"]

	_, shortCircuit, err = plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	_, secondBody := fake.snapshot()

	assert.Equal(t, 2, fake.calls())
	assert.Equal(t, firstKey, secondBody["idempotencyKey"],
		"the same request attempt reserves under the same idempotency key, so a replay answers the original reservation")
	// The replayed reserve is answered under the original key, but the replayed DISPATCH is
	// not let through: since 07.4 the attempt's commitment is already claimed, and one
	// attempt identity dispatches to a provider at most once (dispatch_already_claimed).
	require.NotNil(t, shortCircuit, "a duplicate dispatch of a claimed attempt is refused, not re-dispatched")
	require.NotNil(t, shortCircuit.Error)
	require.NotNil(t, shortCircuit.Error.Type)
	assert.Equal(t, string(DecisionDispatchAlreadyClaimed), *shortCircuit.Error.Type)

	// A fallback chain's next attempt is a different attempt of the same request: a new
	// attempt identity, a new reserve, and - unlike the replay above - a dispatch that is
	// allowed to proceed.
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 1)
	_, shortCircuit, err = plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	_, thirdBody := fake.snapshot()
	assert.NotEqual(t, firstKey, thirdBody["idempotencyKey"],
		"a new attempt of the same request mints a new attempt identity")
}

// --- what the enabled branch does with models that are not paid -------------------------

// Explicitly free work costs nothing and reserves nothing: the enabled branch carries the
// free/unknown distinction the disabled gate deliberately leaves alone.
func TestPreLLMReserveSkipsExplicitlyFreeModels(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reserveFreeModel))

	require.NoError(t, err)
	require.Nil(t, shortCircuit, "an explicitly free model is not paid work")
	assert.Nil(t, ctx.Value(governanceRejectedContextKey))
	assert.Equal(t, 0, fake.calls(), "free work reserves nothing")
}

// Unknown pricing is not free: it goes through the reserve door, where OpenBot's price
// snapshot is the authority that refuses it. The gateway does not guess, either way.
func TestPreLLMReserveSendsUnknownPricingThroughTheDoor(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_basis_not_found"))
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, plugin, ctx, reserveChatRequest(reserveUnknownModel))

	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
	assert.Contains(t, bifrostErr.Error.Message, "price_basis_not_found")
	assert.Equal(t, 1, fake.calls(), "unknown pricing asked the billing service, not the gateway's guess")
}

// A negative rate is the catalog's "unpriced" marker, never a refund and never free
// (review-264 L1): the classification reads it as unknown, so the model cannot slip past
// the seam on the explicitly-free branch.
func TestPreLLMReserveNegativeRateIsNotExplicitlyFree(t *testing.T) {
	plugin := newReserveTestPlugin(t, enabledAdmission())
	assert.Equal(t, pricingUnknown, plugin.classifyPricing(schemas.OpenAI, reserveNegativeModel),
		"any negative rate makes the pricing unknown, not free")

	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	armed := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, armed, ctx, reserveChatRequest(reserveNegativeModel))

	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, 1, fake.calls(), "a negative-rate model goes through the reserve door, not the free branch")
}

// --- the deployment's posture when the seam cannot run ----------------------------------

// An enabled extension with no reserve door wired cannot prove a hold, so dispatchable work
// is refused rather than passed unreserved; explicitly free work still costs nothing.
func TestPreLLMReserveMisconfiguredOrMissingDoorRefusesWithoutCalling(t *testing.T) {
	t.Run("no reserve block", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, enabledAdmission())
		ctx := reserveTestCtx()

		bifrostErr := reserveRefusalFor(t, plugin, ctx, reserveChatRequest(reservePaidModel))

		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
		assert.Contains(t, bifrostErr.Error.Message, "no reserve door is wired")
	})
	t.Run("incomplete reserve block", func(t *testing.T) {
		fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		admission := admissionWithReserve(fake.endpoint(), 0)
		admission.Reserve.ServiceSecret = "" // a door that cannot authenticate is not a door
		plugin := newReserveTestPlugin(t, admission)
		ctx := reserveTestCtx()

		bifrostErr := reserveRefusalFor(t, plugin, ctx, reserveChatRequest(reservePaidModel))

		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
		assert.Equal(t, 0, fake.calls(), "a miswired door is not called: the request is refused on posture")
	})
	t.Run("explicitly free still passes", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, enabledAdmission())
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reserveFreeModel))
		require.NoError(t, err)
		require.Nil(t, shortCircuit)
	})
}

// The attempt identity derivation is a pure function of the attempt it names: the same context
// derivation twice yields the same identity, and nothing about it depends on wall-clock time.
func TestCreditAttemptIDDerivesFromTheAttemptNotTheClock(t *testing.T) {
	ctx := reserveTestCtx()
	first := creditAttemptID(ctx, schemas.OpenAI, reservePaidModel)
	second := creditAttemptID(ctx, schemas.OpenAI, reservePaidModel)
	assert.Equal(t, first, second)

	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 2)
	third := creditAttemptID(ctx, schemas.OpenAI, reservePaidModel)
	assert.NotEqual(t, first, third, "a different attempt of the request derives a different identity")

	other := creditAttemptID(ctx, schemas.OpenAI, reserveFreeModel)
	assert.NotEqual(t, first, other, "a different model of the same attempt derives a different identity")
}
