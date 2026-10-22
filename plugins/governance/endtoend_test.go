package governance

// Bifrost 07.12 (#275): group 07's stitching acceptance, end to end through the hook
// sequence the pipeline performs, with every OpenBot surface a real HTTP server speaking
// the wire contract and the provider a deterministic fake whose call count is the ticket's
// acceptance instrument (五类拒绝场景中供应商调用数均为 0).
//
// THE ORDER THESE TESTS HOLD. The pipeline's stages, in the order a real dispatch runs
// them: PreLLMHook is where admission runs, the output-cap evidence gate refuses an
// uncapped call, and the reserve door is asked for a hold; a short-circuit from it is the
// pipeline's ONLY exit before the provider, so "no provider call" is asserted as the hook
// turning the request away AND the deterministic fake's counter reading zero. The provider
// stage itself is one real POST to the fake — performed only on the no-short-circuit
// branch, the same predicate the pipeline applies. PostLLMHook is 07.8's settlement: the
// actual usage the provider reported is persisted to the result outbox before the hook
// returns, then delivered to OpenBot's Settle exactly once, at the deterministic figures
// the fake answered with — a billed failure rides its error's BilledUsage the same way,
// and an unusable report holds the reservation (Q12) instead of settling a zero.
//
// WHAT IS NOT REBUILT HERE. The per-gate semantics already have their own files (admission,
// reserve, bound evidence, output cap, settlement outbox); this file stitches them into the
// one continuous flow a deployment actually runs, which is the seam the individual tests
// cannot prove: that the gates fire in the pipeline's order, that the doors are asked the
// right number of times, and that a refused request never reaches the provider.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

// deterministicProvider is the ticket's 确定性 provider fake: a real HTTP endpoint that
// answers every chat completion with the SAME usage — 600 prompt tokens of which 100 were
// cached reads, 400 completion tokens — counting each call. The counter is the acceptance
// instrument for the refusal matrix; the fixed usage is the actual the settle message must
// report on the success path.
type deterministicProvider struct {
	server *httptest.Server

	mu    sync.Mutex
	calls int
}

func newDeterministicProvider(t *testing.T) *deterministicProvider {
	t.Helper()
	fake := &deterministicProvider{}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.calls++
		fake.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-e2e","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":600,"completion_tokens":400,"total_tokens":1000,"prompt_tokens_details":{"cached_tokens":100}}}`))
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (p *deterministicProvider) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// providerUsage is the fake's deterministic answer, in the shape the pipeline's provider
// client reports it: the prompt total includes its cached part.
type providerUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	Details          struct {
		CachedReadTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// dispatch performs the pipeline's provider stage for real: one POST of the admitted
// request to the fake, answered with the deterministic usage. It is called ONLY where
// PreLLMHook returned no short-circuit, so the counter reads exactly what a deployment's
// provider would have seen.
func (p *deterministicProvider) dispatch(t *testing.T, req *schemas.BifrostRequest) *schemas.BifrostResponse {
	t.Helper()
	body, err := json.Marshal(req.ChatRequest)
	require.NoError(t, err)
	resp, err := p.server.Client().Post(p.server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode, "the deterministic fake answers 200")
	var completion struct {
		Usage providerUsage `json:"usage"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&completion))

	// The translation the provider client performs: raw completion JSON onto the response
	// object the post hooks read, stamped the way core stamps it before PostLLMHook runs.
	result := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     completion.Usage.PromptTokens,
				CompletionTokens: completion.Usage.CompletionTokens,
				TotalTokens:      completion.Usage.PromptTokens + completion.Usage.CompletionTokens,
				PromptTokensDetails: &schemas.ChatPromptTokensDetails{
					CachedReadTokens: completion.Usage.Details.CachedReadTokens,
				},
			},
		},
	}
	result.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, req.ChatRequest.Model, req.ChatRequest.Model)
	return result
}

// e2eFailWithBilledUsage is the billed-failure translation: the provider processed tokens
// and then the call failed, so the cost is real and rides the error's BilledUsage (07.11).
// The routing fields name the model the attempt ran, the way core stamps them before the
// post hooks read the error.
func e2eFailWithBilledUsage(model string, promptTokens int) *schemas.BifrostError {
	statusCode := 500
	return &schemas.BifrostError{
		StatusCode: &statusCode,
		Error:      &schemas.ErrorField{Message: "provider failed after processing tokens"},
		ExtraFields: schemas.BifrostErrorExtraFields{
			RequestType:            schemas.ChatCompletionRequest,
			Provider:               schemas.OpenAI,
			OriginalModelRequested: model,
			ResolvedModelUsed:      model,
			BilledUsage:            &schemas.BifrostLLMUsage{PromptTokens: promptTokens, TotalTokens: promptTokens},
		},
	}
}

// --- the success path: one reserve, one provider call, one settle at the actual ----------

func TestEndToEndStrictTextBillingChargesTheActualOnce(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))
	provider := newDeterministicProvider(t)

	ctx := reserveTestCtx()
	req := reserveChatRequest(reservePaidModel)

	// Admission held a reservation, so the pipeline dispatches exactly once.
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "a held reservation admits the dispatch")
	result := provider.dispatch(t, req)
	assert.Equal(t, 1, provider.callCount(), "the admitted attempt reached the provider exactly once")
	assert.Equal(t, 1, reserve.calls(), "one reserve per dispatch")

	// 07.8: the actual the provider reported is persisted and delivered before anything
	// else can happen to the attempt.
	_, _, err = plugin.PostLLMHook(ctx, result, nil)
	require.NoError(t, err)
	waitFor(t, func() bool { return door.callCount() == 1 }, "the settle door is called exactly once")
	assert.Equal(t, 1, door.callCount(), "one settle per admitted attempt, never more")

	auth, body := door.snapshot()
	assert.Equal(t, "Bearer "+settleServiceSecret, auth, "the settle authenticates as the trusted billing identity")
	require.NotNil(t, body)
	assert.Equal(t, "res-reserve-1", body["reservationId"], "the reservation is the settle's whole identity: no account, no price")
	assert.Nil(t, body["payer"], "no account field rides the settle")
	assert.Nil(t, body["amount"], "no price rides the settle: OpenBot prices through the locked basis")
	usage, ok := body["actualUsage"].(map[string]any)
	require.True(t, ok, "the settle reports the closed usage vocabulary")
	assert.Equal(t, float64(500), usage["input"], "prompt 600 minus its 100 cached reads")
	assert.Equal(t, float64(400), usage["output"], "the completion the provider reported, inside the reserved cap")
	assert.Equal(t, float64(100), usage["cache_read"])

	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "a delivered settlement leaves nothing in the retry queue")
}

// --- a billed failure charges the actual; an unusable report holds (Q12) ------------------

func TestEndToEndBilledFailureChargesTheActualAndUnknownHolds(t *testing.T) {
	t.Run("a failed attempt whose provider billed usage still settles it", func(t *testing.T) {
		reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		door := newFakeSettleDoor(t, http.StatusOK)
		plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))
		provider := newDeterministicProvider(t)

		ctx := reserveTestCtx()
		req := reserveChatRequest(reservePaidModel)
		_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
		require.NoError(t, err)
		require.Nil(t, shortCircuit)
		provider.dispatch(t, req) // the tokens were processed before the call failed

		_, _, err = plugin.PostLLMHook(ctx, nil, e2eFailWithBilledUsage(reservePaidModel, 40))
		require.NoError(t, err)
		waitFor(t, func() bool { return door.callCount() == 1 }, "the billed failure settles its actual")
		_, body := door.snapshot()
		require.NotNil(t, body)
		usage, ok := body["actualUsage"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, float64(40), usage["input"], "the error's billed usage is the actual that is charged")
	})

	t.Run("a success with no usable usage report holds the reservation", func(t *testing.T) {
		reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		door := newFakeSettleDoor(t, http.StatusOK)
		plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))
		provider := newDeterministicProvider(t)

		ctx := reserveTestCtx()
		req := reserveChatRequest(reservePaidModel)
		_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
		require.NoError(t, err)
		require.Nil(t, shortCircuit)
		provider.dispatch(t, req)

		// The report was lost on the way back: the response carries no usage at all.
		unusable := &schemas.BifrostResponse{ChatResponse: &schemas.BifrostChatResponse{}}
		unusable.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, reservePaidModel, reservePaidModel)
		_, _, err = plugin.PostLLMHook(ctx, unusable, nil)
		require.NoError(t, err)
		plugin.wg.Wait()

		assert.Equal(t, 0, door.callCount(), "an unknown result is never settled as a zero")
		pending, err := plugin.settlementOutbox.Pending(ctx)
		require.NoError(t, err)
		require.Len(t, pending, 1, "the unknown result is persisted for recovery, never dropped")
		assert.Equal(t, SettlementOutcomeUsageUnknown, pending[0].Outcome)
	})
}

// --- the five refusal classes: the provider is never called -------------------------------

func TestEndToEndFiveRefusalClassesCallNoProvider(t *testing.T) {
	// insufficient: OpenBot's reserve door refuses the hold (余额不足).
	t.Run("insufficient credits", func(t *testing.T) {
		reserve := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("insufficient_credits"))
		door := newFakeSettleDoor(t, http.StatusOK)
		plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))
		provider := newDeterministicProvider(t)

		bifrostErr := reserveRefusalFor(t, plugin, reserveTestCtx(), reserveChatRequest(reservePaidModel))
		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
		assert.Equal(t, 1, reserve.calls(), "the door was asked, and refused")
		assert.Equal(t, 0, provider.callCount(), "no provider call on a refused hold")
		assert.Equal(t, 0, door.callCount(), "nothing to settle")
	})

	// timeout: the reserve door never answers in time (超时).
	t.Run("reserve door timeout", func(t *testing.T) {
		reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		reserve.delay = 300 * time.Millisecond
		door := newFakeSettleDoor(t, http.StatusOK)
		plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 50))
		provider := newDeterministicProvider(t)

		bifrostErr := reserveRefusalFor(t, plugin, reserveTestCtx(), reserveChatRequest(reservePaidModel))
		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
		assert.Contains(t, bifrostErr.Error.Message, "timed out")
		assert.Equal(t, 0, provider.callCount(), "an unanswered reserve admits no dispatch")
		assert.Equal(t, 0, door.callCount())
	})

	// revoked: the credential the request presented resolves to nothing (撤销).
	t.Run("revoked credential", func(t *testing.T) {
		reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		door := newFakeSettleDoor(t, http.StatusOK)
		plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))
		provider := newDeterministicProvider(t)

		ctx := presentCtx(refusalTestUnknownVKValue)
		ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-e2e-revoked")
		ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 0)
		bifrostErr := reserveRefusalFor(t, plugin, ctx, reserveChatRequest(reservePaidModel))
		require.NotNil(t, bifrostErr.StatusCode)
		assert.Equal(t, 401, *bifrostErr.StatusCode, "a revoked credential is failed authentication")
		assert.Equal(t, 0, reserve.calls(), "the door is never asked for a request that did not authenticate")
		assert.Equal(t, 0, provider.callCount())
		assert.Equal(t, 0, door.callCount())
	})

	// no bound evidence: the call caps no output, so no hold could be priced (缺上限证据).
	t.Run("call without an enforceable output cap", func(t *testing.T) {
		reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		door := newFakeSettleDoor(t, http.StatusOK)
		plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))
		provider := newDeterministicProvider(t)

		req := reserveChatRequest(reservePaidModel)
		req.ChatRequest.Params.MaxCompletionTokens = nil
		_, shortCircuit, err := plugin.PreLLMHook(reserveTestCtx(), req)
		require.NoError(t, err)
		require.NotNil(t, shortCircuit, "an uncapped call is not dispatched")
		require.NotNil(t, shortCircuit.Error.Error.Code)
		assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code)
		assert.Equal(t, 0, reserve.calls(), "the door is never asked for an uncapped call")
		assert.Equal(t, 0, provider.callCount())
		assert.Equal(t, 0, door.callCount())
	})

	// extension missing: the admission extension is not configured at all (扩展缺失).
	t.Run("admission extension not configured", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, nil)
		provider := newDeterministicProvider(t)

		bifrostErr := reserveRefusalFor(t, plugin, reserveTestCtx(), reserveChatRequest(reservePaidModel))
		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditAdmissionNotEnabled), *bifrostErr.Type,
			"a deployment without the extension refuses paid work rather than serving it unreserved")
		assert.Equal(t, 0, provider.callCount())
	})
}
