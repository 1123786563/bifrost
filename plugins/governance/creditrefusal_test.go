package governance

// The refusal semantics of the credit system (07.3): a refusal is a SHORT CIRCUIT, not a plain
// plugin error. What that means, and what every test below holds the refusal to:
//
//   - The refusal reaches the pipeline as an LLMPluginShortCircuit whose error carries
//     AllowFallbacks=false. Core treats a pre-hook short circuit as final (it returns the
//     error to the caller without dispatching), and the flag makes the refusal self-describing
//     at every other layer that reads a BifrostError: there is no configuration in which a
//     request the credit system refused is retried against another provider. A plain error
//     return would leave the decision to the caller's error handling; the short-circuit type
//     and the flag take that decision away.
//
//   - The refusal's error carries ONE machine-readable reason code in Error.Code, drawn from
//     the gateway's closed five-code vocabulary - the five categories a caller or operator
//     acts on: insufficient_balance (余额不足), authorization_invalid (授权失效),
//     price_unknown (价格未知), cap_not_provable (上限不可证明), admission_unreachable
//     (准入不可达). Every ending of the reserve seam maps onto exactly one category, and the
//     five categories are pairwise distinguishable: five different reasons never share a code.
//
//   - The refusal produces no dispatch commitment (no reservation is stamped on the context -
//     there is nothing to settle against) and no settlement record (the refused mark keeps
//     PostLLM's accounting from writing any budget or rate-limit movement).
//
//   - The refusal response leaks nothing: not the trusted service secret, not the caller's
//     Authorization credential, not the billing service's answer body (which carries OpenBot
//     account data that belongs to no bot client), and none of the deployment's account
//     identifiers.
//
// The reason-code tests fail before 07.3 because the refusal answered with no code at all: the
// five categories existed only as prose inside the message.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// creditRefusalTestVKValue is the key value the refusal-semantics tests' store serves.
const creditRefusalTestVKValue = "sk-bf-refusal-vk"

// The gateway's five refusal reason codes: the closed vocabulary every credit refusal answers
// with, one per category a caller or operator acts on. The tests hold the production constants
// to exactly these strings.
var creditRefusalReasonCodesUnderTest = []string{
	"insufficient_balance",
	"authorization_invalid",
	"price_unknown",
	"cap_not_provable",
	"admission_unreachable",
}

// refusalReasonCode extracts the machine-readable reason code a refusal answered with, failing
// the test when the refusal carries none: a refusal without a reason code is the bug 07.3
// exists to close.
func refusalReasonCode(t *testing.T, bifrostErr *schemas.BifrostError) string {
	t.Helper()
	require.NotNil(t, bifrostErr, "the refusal carries an error")
	require.NotNil(t, bifrostErr.Error, "the refusal carries an error field")
	require.NotNil(t, bifrostErr.Error.Code,
		"a credit refusal carries a machine-readable reason code in error.code; without it the five categories are prose only")
	return *bifrostErr.Error.Code
}

// refusalForDoorAnswer runs a paid chat request through the LLM hook against a fake billing
// service answering the given 422 refusal code, and returns the error the caller is answered
// with. The refusal must be the hook's ONLY exit: no plain error return, nothing that leaves
// the dispatch decision to the caller.
func refusalForDoorAnswer(t *testing.T, code string) (*schemas.BifrostError, *fakeCreditService) {
	t.Helper()
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer(code))
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err, "a refusal is a short circuit, never a plugin error")
	require.NotNil(t, shortCircuit, "the request reached the provider instead of being turned away")
	require.NotNil(t, shortCircuit.Error)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
		"a refused request is marked refused for everything downstream")

	return shortCircuit.Error, fake
}

// --- the five reason codes are distinguishable ------------------------------------------

func TestCreditRefusalCarriesTheReasonCodeForBalanceExhaustion(t *testing.T) {
	bifrostErr, _ := refusalForDoorAnswer(t, "insufficient_credits")

	assert.Equal(t, "insufficient_balance", refusalReasonCode(t, bifrostErr),
		"a spent-out account is answered with the balance code, whatever the door's own vocabulary says")
}

func TestCreditRefusalCarriesTheReasonCodeForAuthorizationFailures(t *testing.T) {
	for _, doorCode := range []string{"grant_invalid", "payer_unresolved", "payer_conflict", "model_not_open"} {
		t.Run(doorCode, func(t *testing.T) {
			bifrostErr, _ := refusalForDoorAnswer(t, doorCode)

			assert.Equal(t, "authorization_invalid", refusalReasonCode(t, bifrostErr),
				"an unusable grant or payer is answered with the authorization code")
		})
	}
}

func TestCreditRefusalCarriesTheReasonCodeForUnknownPricing(t *testing.T) {
	for _, doorCode := range []string{"price_unknown", "price_basis_not_found", "catalog_never_synced", "catalog_stale"} {
		t.Run(doorCode, func(t *testing.T) {
			bifrostErr, _ := refusalForDoorAnswer(t, doorCode)

			assert.Equal(t, "price_unknown", refusalReasonCode(t, bifrostErr),
				"pricing the billing service cannot vouch for is answered with the price-unknown code")
		})
	}
}

func TestCreditRefusalCarriesTheReasonCodeForAnUnprovableCeiling(t *testing.T) {
	bifrostErr, _ := refusalForDoorAnswer(t, "bound_evidence_not_enforceable")

	assert.Equal(t, "cap_not_provable", refusalReasonCode(t, bifrostErr),
		"a call whose ceiling cannot be enforced is answered with the cap code")
}

func TestCreditRefusalCarriesTheReasonCodeForAnUnreachableAdmission(t *testing.T) {
	t.Run("door says the request itself was invalid", func(t *testing.T) {
		bifrostErr, _ := refusalForDoorAnswer(t, "request_invalid")
		assert.Equal(t, "admission_unreachable", refusalReasonCode(t, bifrostErr))
	})
	t.Run("door reports a reservation conflict", func(t *testing.T) {
		bifrostErr, _ := refusalForDoorAnswer(t, "reservation_conflict")
		assert.Equal(t, "admission_unreachable", refusalReasonCode(t, bifrostErr))
	})
	t.Run("door answers a code the vocabulary does not carry", func(t *testing.T) {
		bifrostErr, _ := refusalForDoorAnswer(t, "some_future_code")
		assert.Equal(t, "admission_unreachable", refusalReasonCode(t, bifrostErr),
			"an unrecognized code stays inside the closed vocabulary rather than opening a sixth category")
	})
	t.Run("door times out", func(t *testing.T) {
		held := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		held.delay = 300 * time.Millisecond
		plugin := newReserveTestPlugin(t, admissionWithReserve(held.endpoint(), 50))
		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		assert.Equal(t, "admission_unreachable", refusalReasonCode(t, shortCircuit.Error),
			"an unanswered reserve is the admission being unreachable, not the caller failing")
	})
	t.Run("door unreachable", func(t *testing.T) {
		dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		dead.Close() // the URL now answers nothing
		plugin := newReserveTestPlugin(t, admissionWithReserve(dead.URL+"/reserve", 0))
		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		assert.Equal(t, "admission_unreachable", refusalReasonCode(t, shortCircuit.Error))
	})
	t.Run("door answers a server error", func(t *testing.T) {
		fake := newFakeCreditService(t, http.StatusInternalServerError, `{"error":"boom"}`)
		plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		assert.Equal(t, "admission_unreachable", refusalReasonCode(t, shortCircuit.Error))
	})
	t.Run("door answers a 201 that holds nothing readable", func(t *testing.T) {
		fake := newFakeCreditService(t, http.StatusCreated, `{"unexpected":"shape"}`)
		plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		assert.Equal(t, "admission_unreachable", refusalReasonCode(t, shortCircuit.Error))
	})
	t.Run("no reserve door wired at all", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, enabledAdmission())
		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		assert.Equal(t, "admission_unreachable", refusalReasonCode(t, shortCircuit.Error))
	})
	t.Run("admission extension not enabled", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, nil)
		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		assert.Equal(t, "admission_unreachable", refusalReasonCode(t, shortCircuit.Error),
			"without the extension the admission machinery is unavailable, whatever the pricing says")
	})
}

// The distinguishability acceptance criterion, whole: the vocabulary carries exactly five codes,
// and one ending per category answers with one code of its own - five different reasons never
// share a code, and a refused request never sees a code outside the closed vocabulary.
func TestCreditRefusalReasonCodesSeparateAllFiveCategories(t *testing.T) {
	// One representative ending per category, run through the hook.
	representatives := map[string]string{ // category -> door answer or local ending
		"insufficient_balance":  "insufficient_credits",
		"authorization_invalid": "grant_invalid",
		"price_unknown":         "price_unknown",
		"cap_not_provable":      "bound_evidence_not_enforceable",
		"admission_unreachable": "request_invalid",
	}

	answered := map[string]bool{}
	for expected, doorCode := range representatives {
		bifrostErr, _ := refusalForDoorAnswer(t, doorCode)
		code := refusalReasonCode(t, bifrostErr)
		assert.Equal(t, expected, code, "the %s category answers with its own code", expected)
		require.False(t, answered[code], "reason code %q is shared by two categories", code)
		answered[code] = true
	}
	assert.Len(t, answered, 5, "five categories, five distinct codes")
}

// --- the refusal stops the chain ---------------------------------------------------------

// A refusal is final: no provider sees the request, and no fallback attempt after it either.
// Core already returns a pre-hook short circuit without dispatching; the refusal's error ALSO
// carries AllowFallbacks=false, so every layer that reads the error reads the same answer -
// there is nothing to retry this request against.
func TestCreditRefusalShortCircuitStopsTheFallbackChain(t *testing.T) {
	t.Run("reserve refused", func(t *testing.T) {
		bifrostErr, _ := refusalForDoorAnswer(t, "insufficient_credits")
		require.NotNil(t, bifrostErr.AllowFallbacks, "the refusal declares its own fallback posture")
		assert.False(t, *bifrostErr.AllowFallbacks,
			"a refused request is not retried against another provider")
	})
	t.Run("reserve unanswered", func(t *testing.T) {
		held := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		held.delay = 300 * time.Millisecond
		plugin := newReserveTestPlugin(t, admissionWithReserve(held.endpoint(), 50))
		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		require.NotNil(t, shortCircuit.Error.AllowFallbacks)
		assert.False(t, *shortCircuit.Error.AllowFallbacks)
	})
	t.Run("no door wired", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, enabledAdmission())
		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		require.NotNil(t, shortCircuit.Error.AllowFallbacks)
		assert.False(t, *shortCircuit.Error.AllowFallbacks)
	})
	t.Run("admission not enabled", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, nil)
		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		require.NotNil(t, shortCircuit.Error.AllowFallbacks)
		assert.False(t, *shortCircuit.Error.AllowFallbacks)
	})
}

// --- no dispatch commitment, no settlement record ----------------------------------------

// refusalSettlementFixture arms the reserve seam over a virtual key that also carries a budget
// and a rate limit, so the settlement surfaces a refused request must leave untouched are
// observable: if the accounting wrote anything, these counters move.
type refusalSettlementFixture struct {
	plugin *GovernancePlugin
	store  GovernanceStore
}

// newRefusalSettlementFixture is newReserveTestPlugin over a key that answers to a budget and a
// rate limit as well as the provider allowlist, with the pricing rows the reserve tests use.
func newRefusalSettlementFixture(t *testing.T, admission *CreditAdmissionConfig) *refusalSettlementFixture {
	t.Helper()
	vk := buildVirtualKeyWithProviders("vk-refusal", creditRefusalTestVKValue, "refusal-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai",
			[]string{reservePaidModel, reserveFreeModel, reserveNegativeModel, reserveUnknownModel})})
	budget := buildBudgetWithUsage("refusal-budget", 1_000_000.0, 0.0, "1d")
	vkID := "vk-refusal"
	budget.VirtualKeyID = &vkID
	vk.Budgets = append(vk.Budgets, *budget)
	rl := buildRateLimit("refusal-rl", 1_000_000, 1_000_000)
	vk.RateLimit = rl
	rlID := rl.ID
	vk.RateLimitID = &rlID

	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
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
	return &refusalSettlementFixture{plugin: plugin, store: store}
}

// refusalSettlementCtx is a request context presenting the fixture's key, the way the pipeline
// carries one into PreLLMHook.
func refusalSettlementCtx() *schemas.BifrostContext {
	ctx := presentCtx(creditRefusalTestVKValue)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-refusal-1")
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 0)
	return ctx
}

func (f *refusalSettlementFixture) budgetUsage() float64 {
	return f.store.GetGovernanceData(context.Background()).Budgets["refusal-budget"].CurrentUsage
}

func (f *refusalSettlementFixture) requestCount() int64 {
	return f.store.GetGovernanceData(context.Background()).RateLimits["refusal-rl"].RequestCurrentUsage
}

// A refused request produces no dispatch commitment and no settlement record: no reservation is
// stamped on the context (there is nothing held to settle against), the door was asked exactly
// once, and driving PostLLM the way core does after a pre-hook short circuit - same context,
// same refusal error - writes nothing to the budget or the request counter.
func TestCreditRefusalShortCircuitLeavesNoDispatchCommitmentAndSettlesNothing(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("insufficient_credits"))
	f := newRefusalSettlementFixture(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := refusalSettlementCtx()

	_, shortCircuit, err := f.plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit)

	assert.Nil(t, ctx.Value(CreditReservationContextKey),
		"a refusal stamps no reservation on the context: no dispatch commitment exists")
	assert.Equal(t, 1, fake.calls(), "the door was asked once; nothing else was contacted")

	// Core runs the post hooks with the short-circuit's error after a pre-hook refusal; the
	// refused mark is the only thing between that call and the accounting worker. Whatever it
	// is handed, a refused request settles nothing.
	_, postErr, hookErr := f.plugin.PostLLMHook(ctx, nil, shortCircuit.Error)
	require.NoError(t, hookErr)
	assert.Same(t, shortCircuit.Error, postErr,
		"the refusal the caller sees is the refusal the hook answered with")

	time.Sleep(250 * time.Millisecond) // let any (there must be none) async accounting settle
	assert.Equal(t, 0.0, f.budgetUsage(), "a refused request moves no budget")
	assert.Equal(t, int64(0), f.requestCount(), "a refused request counts no request")
}

// The control arm: the same fixture over an ALLOWED request does settle, so the zero above is
// the refusal's doing, not a fixture that cannot measure.
func TestCreditRefusalSettlementFixtureMeasuresAnAllowedRequest(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	f := newRefusalSettlementFixture(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := refusalSettlementCtx()

	_, shortCircuit, err := f.plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)

	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{PromptTokens: 60, CompletionTokens: 40, TotalTokens: 100},
		},
	}
	// Core stamps these on every response before the post hooks read them.
	response.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, reservePaidModel, reservePaidModel)
	_, _, hookErr := f.plugin.PostLLMHook(ctx, response, nil)
	require.NoError(t, hookErr)

	time.Sleep(250 * time.Millisecond)
	assert.Greater(t, f.budgetUsage(), 0.0, "the fixture measures an allowed request settling")
	assert.Greater(t, f.requestCount(), int64(0), "the fixture counts an allowed request")
}

// --- the refusal leaks nothing -----------------------------------------------------------

// The refusal is answered to a bot client. It carries the reason code and the model; it must
// not carry the trusted service secret, the Authorization credential, the billing service's
// answer body (whose detail fields carry OpenBot account data that belongs to no bot client),
// or the deployment's own account identifiers.
func TestCreditRefusalResponseLeaksNoCredentialOrAccountData(t *testing.T) {
	body := `{"error":"refused","code":"insufficient_credits","detail":{"holderAccount":"acct-OTHER-999","reservationId":"res-someone-else"}}`
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, body)
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit)

	serialized, err := json.Marshal(shortCircuit.Error)
	require.NoError(t, err)
	wire := string(serialized)

	assert.NotContains(t, wire, reserveServiceSecret,
		"the service secret never appears in a refusal response")
	assert.NotContains(t, wire, "Bearer", "the credential shape never appears in a refusal response")
	assert.NotContains(t, wire, "acct-OTHER-999",
		"the billing service's answer body (others' account data) is never echoed into the response")
	assert.NotContains(t, wire, "res-someone-else",
		"no reservation id but the request's own is ever named")
	assert.NotContains(t, wire, "grant-reserve-1",
		"the deployment's grant identity is not the bot client's business")
	assert.NotContains(t, wire, "user-reserve-1",
		"the deployment's payer identity is not the bot client's business")
	assert.Contains(t, wire, "insufficient_balance",
		"what the refusal does carry is the closed reason code")
}
