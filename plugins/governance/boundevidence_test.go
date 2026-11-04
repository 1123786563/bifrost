package governance

// The per-request bound evidence (07.6): what a reserve may carry as proof that THIS call's
// cost ceiling can be enforced, derived from the request the funnel is about to dispatch
// rather than from deployment configuration (ADR 0003: only a call whose ceiling can be
// ENFORCED is admitted; ADR 0001: the conservative count is a rejection aid, never evidence,
// never a price).
//
// Three properties the tests below hold the seam to, one per admission-criterion line:
//
//   - The input count covers EVERY billable input, because it counts the complete serialized
//     request - messages, system prompt, tool definitions, attachments - plus the extra
//     params the adapters forward verbatim. Anything the count does not see is a hole a
//     caller could bill through, so "cannot count in full" is itself evidence insufficiency.
//   - A call whose output side carries no provider-enforced cap (no max_tokens) is refused
//     before the reserve door is even asked: an uncapped generation is an unbounded cost,
//     and "the model usually stops" is not a ceiling.
//   - The evidence on the wire carries mechanisms and ceilings only: the configured input
//     ceiling and the request's own max_tokens. The conservative count never rides the
//     reserve, never sets the hold, and no estimate-shaped entry can - the estimate may only
//     make the gate refuse, never let it through.
//
// Every refusal here lands in the same short-circuit shape as every other credit refusal:
// the request is marked refused, no provider is called, and the reason names the model and
// a caller-actionable cause without leaking the deployment's credentials or account data.

import (
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// boundTestInputCeiling is the deployment's configured conservative input ceiling the bound
// tests arm the reserve door with: generous for the small fixtures, deliberately tight in
// the tests that squeeze under it.
const boundTestInputCeiling = int64(2000)

// boundTestMaxTokens is the output cap the well-formed fixtures carry: the provider-enforced
// max_tokens the reserve's output evidence attests.
const boundTestMaxTokens = 4096

// admissionWithInputBound arms the extension with a reserve door and the deployment's input
// ceiling, plus verified output-cap enforcement evidence for the fixture models (07.7): these
// tests exercise the per-request bound derivation, which sits one gate downstream of the
// model-cell evidence the output-cap tests own. There is no configured bound-evidence list
// any more: what the reserve carries is derived from each request, never from deployment
// configuration.
func admissionWithInputBound(endpoint string, inputCeiling int64) *CreditAdmissionConfig {
	enabled := true
	return &CreditAdmissionConfig{
		Enabled:           &enabled,
		OutputCapEvidence: verifiedOutputCapEvidence(),
		Reserve: &CreditReserveConfig{
			Endpoint:           endpoint,
			ServiceSecret:      reserveServiceSecret,
			GrantID:            "grant-reserve-1",
			PayerKind:          "personal",
			PayerID:            "user-reserve-1",
			SnapshotVersion:    7,
			RateVersion:        3,
			InputCeilingTokens: inputCeiling,
			MaximumCredits:     100,
		},
	}
}

// boundChatRequest builds an empty chat request for the model named, the shape the LLM funnel
// carries; the per-test builders below layer messages, tools and the output cap onto it.
func boundChatRequest(model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: model},
	}
}

// boundAddMessage appends one chat message with the given role and string content.
func boundAddMessage(req *schemas.BifrostRequest, role schemas.ChatMessageRole, content string) *schemas.BifrostRequest {
	text := content
	req.ChatRequest.Input = append(req.ChatRequest.Input, schemas.ChatMessage{
		Role: role,
		Content: &schemas.ChatMessageContent{
			ContentStr: &text,
		},
	})
	return req
}

// boundAddTool appends one function-tool definition whose schema text has the given size, the
// billable input a naive per-message counter misses.
func boundAddTool(req *schemas.BifrostRequest, name, description string) *schemas.BifrostRequest {
	if req.ChatRequest.Params == nil {
		req.ChatRequest.Params = &schemas.ChatParameters{}
	}
	req.ChatRequest.Params.Tools = append(req.ChatRequest.Params.Tools, schemas.ChatTool{
		Type: schemas.ChatToolTypeFunction,
		Function: &schemas.ChatToolFunction{
			Name:        name,
			Description: &description,
		},
	})
	return req
}

// boundAddAttachment appends one multimodal content block carrying file data, the other class
// of billable input a per-message counter misses.
func boundAddAttachment(req *schemas.BifrostRequest, fileData string) *schemas.BifrostRequest {
	req.ChatRequest.Input = append(req.ChatRequest.Input, schemas.ChatMessage{
		Role: schemas.ChatMessageRoleUser,
		Content: &schemas.ChatMessageContent{
			ContentBlocks: []schemas.ChatContentBlock{{
				Type: schemas.ChatContentBlockTypeFile,
				File: &schemas.ChatInputFile{FileData: &fileData},
			}},
		},
	})
	return req
}

// boundWithOutputCap sets the request's provider-enforced output cap the way a caller would.
func boundWithOutputCap(req *schemas.BifrostRequest, maxTokens int) *schemas.BifrostRequest {
	if req.ChatRequest.Params == nil {
		req.ChatRequest.Params = &schemas.ChatParameters{}
	}
	req.ChatRequest.Params.MaxCompletionTokens = &maxTokens
	return req
}

// a well-formed small conversation: system prompt, one old turn, the latest user turn, an
// output cap. It fits far under the generous test ceiling.
func boundWellFormedRequest() *schemas.BifrostRequest {
	req := boundChatRequest(reservePaidModel)
	boundAddMessage(req, schemas.ChatMessageRoleSystem, "you are helpful")
	boundAddMessage(req, schemas.ChatMessageRoleUser, "old question")
	boundAddMessage(req, schemas.ChatMessageRoleAssistant, "old answer")
	boundAddMessage(req, schemas.ChatMessageRoleUser, "new question")
	return boundWithOutputCap(req, boundTestMaxTokens)
}

// --- the call shapes this ticket does not enable ------------------------------------------

// A call method whose shape the gateway cannot count in full stays refused - interface
// existence is not enablement evidence (Q11/Q20). A passthrough body goes to the provider
// as the caller wrote it, and a raw-body dispatch likewise bypasses the typed request the
// counter reads: neither can be serialized, truncated, or proven, so neither is enabled,
// whatever the model's price says.
func TestBoundEvidenceRefusesCallShapesWithoutCompleteSerialization(t *testing.T) {
	fakes := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithInputBound(fakes.endpoint(), boundTestInputCeiling))

	t.Run("passthrough body", func(t *testing.T) {
		ctx := reserveTestCtx()
		req := &schemas.BifrostRequest{
			RequestType:        schemas.PassthroughRequest,
			PassthroughRequest: &schemas.BifrostPassthroughRequest{Provider: schemas.OpenAI, Body: []byte(`{"model":"` + reservePaidModel + `"}`)},
		}

		bifrostErr := reserveRefusalFor(t, plugin, ctx, req)

		require.NotNil(t, bifrostErr.Error.Code)
		assert.Equal(t, CreditRefusalCapNotProvable, *bifrostErr.Error.Code,
			"a body the gateway cannot count in full has no provable ceiling")
		assert.Equal(t, 0, fakes.calls())
	})
	t.Run("raw request body", func(t *testing.T) {
		ctx := reserveTestCtx()
		ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
		req := boundWellFormedRequest()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
		require.NoError(t, err)
		require.NotNil(t, shortCircuit,
			"a dispatch that sends the raw bytes bypasses the counted request and is not enabled")
		require.NotNil(t, shortCircuit.Error.Error)
		require.NotNil(t, shortCircuit.Error.Error.Code)
		assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code)
		assert.Equal(t, 0, fakes.calls())
	})
}

// --- the input count covers every billable input ----------------------------------------

func TestBoundEvidenceCountCoversToolsAndAttachments(t *testing.T) {
	base := boundWellFormedRequest()
	loaded := boundWellFormedRequest()
	for i := range 5 {
		boundAddTool(loaded, "tool-"+string(rune('a'+i)), strings.Repeat("d", 200))
	}
	boundAddAttachment(loaded, "data:text/plain;base64,"+strings.Repeat("Z", 300))

	withTools := countChatRequestInputTokens(loaded.ChatRequest)
	withoutTools := countChatRequestInputTokens(base.ChatRequest)

	assert.Greater(t, withTools, withoutTools,
		"tool definitions and attachments are billable input: counting them must move the count")
	// The conservative direction (ADR 0001): the count must sit above what a typical BPE
	// tokenizer (>=4 bytes/token) would answer for the same serialized request.
	serialized, err := json.Marshal(loaded.ChatRequest)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, withTools, int64(len(serialized)/4),
		"the conservative count never undercuts a typical tokenizer by construction")

	// Fail-closed on the count, too: an input that cannot be fully serialized counts as
	// unbounded, which the admission treats as evidence insufficiency, never as small.
	circular := map[string]any{}
	circular["self"] = circular
	uncountable := boundChatRequest(reservePaidModel)
	uncountable.ChatRequest.Params = &schemas.ChatParameters{
		ExtraParams: map[string]any{"circular": circular},
	}
	assert.Equal(t, int64(math.MaxInt64), countChatRequestInputTokens(uncountable.ChatRequest),
		"an unserializable input is uncountable, and uncountable is unbounded, not zero")
}

// A request whose tools alone push it past the ceiling is refused even though its messages
// would fit: dropping every droppable message must not sneak an oversized tool set through,
// which is exactly the hole a count that skipped tool definitions would open.
func TestBoundEvidenceRefusesWhenToolsExceedTheCeilingAlone(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithInputBound(fake.endpoint(), 400))
	ctx := reserveTestCtx()

	req := boundChatRequest(reservePaidModel)
	boundAddMessage(req, schemas.ChatMessageRoleSystem, "sys")
	boundAddMessage(req, schemas.ChatMessageRoleUser, "question")
	for i := range 5 {
		boundAddTool(req, "tool-"+string(rune('a'+i)), strings.Repeat("d", 300))
	}
	boundWithOutputCap(req, boundTestMaxTokens)

	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "an input that cannot fit under the ceiling even without its droppable messages is not admitted")
	require.NotNil(t, shortCircuit.Error.Error)
	require.NotNil(t, shortCircuit.Error.Error.Code)
	assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code,
		"bounds that cannot be enforced are a cap_not_provable refusal")
	assert.Equal(t, 0, fake.calls(),
		"the door is never asked for a call whose input evidence is insufficient")
}

// --- the output cap must be provider-enforced ---------------------------------------------

func TestBoundEvidenceRefusesCallWithoutEnforceableOutputCap(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithInputBound(fake.endpoint(), boundTestInputCeiling))
	ctx := reserveTestCtx()

	req := boundWellFormedRequest()
	req.ChatRequest.Params.MaxCompletionTokens = nil // the caller capped nothing

	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "a call with no provider-enforced output cap is not dispatched")
	require.NotNil(t, shortCircuit.Error.Error)
	require.NotNil(t, shortCircuit.Error.Error.Code)
	assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code,
		"an uncapped output is a cap_not_provable refusal, not a smaller hold")
	assert.Contains(t, shortCircuit.Error.Error.Message, "max_tokens",
		"the refusal tells the caller what would make the call admissible")
	assert.Contains(t, shortCircuit.Error.Error.Message, reservePaidModel,
		"the refusal names the model it refuses")
	assert.NotContains(t, shortCircuit.Error.Error.Message, reserveServiceSecret,
		"the refusal never carries the trusted service credential")
	assert.Equal(t, 0, fake.calls(), "the door is never asked for an uncapped call")
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
		"a refused request is marked refused for everything downstream")
}

// A legacy max_tokens rides the extra params some callers still send; it is the same
// provider-enforced cap and the same evidence.
func TestBoundEvidenceAcceptsLegacyMaxTokensAsTheOutputCap(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithInputBound(fake.endpoint(), boundTestInputCeiling))
	ctx := reserveTestCtx()

	req := boundWellFormedRequest()
	req.ChatRequest.Params.MaxCompletionTokens = nil
	req.ChatRequest.Params.ExtraParams = map[string]any{"max_tokens": boundTestMaxTokens}

	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "a caller-set max_tokens, however carried, caps the output")

	_, body := fake.snapshot()
	evidence, ok := body["boundEvidence"].([]any)
	require.True(t, ok, "the reserve carries bound evidence")
	assert.Contains(t, evidence, map[string]any{"kind": "provider_max_tokens", "maximumTokens": float64(boundTestMaxTokens)},
		"the output evidence attests the cap the request itself carries")
}

// --- truncation is the self-enforced input bound -------------------------------------------

func TestBoundEvidenceTruncationKeepsSystemAndLatest(t *testing.T) {
	const ceiling = int64(400)
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithInputBound(fake.endpoint(), ceiling))
	ctx := reserveTestCtx()

	req := boundChatRequest(reservePaidModel)
	boundAddMessage(req, schemas.ChatMessageRoleSystem, "you are helpful")
	boundAddMessage(req, schemas.ChatMessageRoleUser, strings.Repeat("o", 120))
	boundAddMessage(req, schemas.ChatMessageRoleAssistant, strings.Repeat("a", 120))
	boundAddMessage(req, schemas.ChatMessageRoleUser, strings.Repeat("q", 300))
	boundWithOutputCap(req, boundTestMaxTokens)
	originals := req.ChatRequest.Input

	require.Greater(t, countChatRequestInputTokens(req.ChatRequest), ceiling,
		"the fixture starts over the ceiling, or the test proves nothing")

	returned, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "a request that fits after dropping old messages is admitted")

	fitted := returned.ChatRequest.Input
	require.NotEmpty(t, fitted)
	assert.Equal(t, schemas.ChatMessageRoleSystem, fitted[0].Role, "the system block is always kept")
	assert.Equal(t, originals[len(originals)-1], fitted[len(fitted)-1], "the latest message is always kept")
	assert.Less(t, len(fitted), len(originals), "the oldest non-system messages are what got dropped")
	assert.LessOrEqual(t, countChatRequestInputTokens(returned.ChatRequest), ceiling,
		"the request that goes on to dispatch is the request that fits the ceiling")

	// The evidence the reserve carries is the MECHANISM's ceiling - the configured input
	// bound - not this request's count.
	_, body := fake.snapshot()
	evidence, ok := body["boundEvidence"].([]any)
	require.True(t, ok)
	assert.Contains(t, evidence, map[string]any{"kind": "self_enforced_input_truncation", "maximumTokens": float64(ceiling)},
		"the input evidence attests the enforced ceiling, never the count")
}

func TestBoundEvidenceRefusesWhenTruncationCannotFit(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithInputBound(fake.endpoint(), 300))
	ctx := reserveTestCtx()

	// system + one huge latest message: nothing is droppable, and the pair that must stay
	// does not fit. There is no self-enforced bound for this call, so it is not enabled.
	req := boundChatRequest(reservePaidModel)
	boundAddMessage(req, schemas.ChatMessageRoleSystem, "you are helpful")
	boundAddMessage(req, schemas.ChatMessageRoleUser, strings.Repeat("q", 2000))
	boundWithOutputCap(req, boundTestMaxTokens)

	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "a call whose system+latest cannot fit the ceiling has no enforceable input bound")
	require.NotNil(t, shortCircuit.Error.Error)
	require.NotNil(t, shortCircuit.Error.Error.Code)
	assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code)
	assert.Equal(t, 0, fake.calls())
}

// --- the estimate is never the proof --------------------------------------------------------

func TestBoundEvidenceEstimateIsNeverTheProof(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithInputBound(fake.endpoint(), boundTestInputCeiling))
	ctx := reserveTestCtx()

	req := boundWellFormedRequest()
	count := countChatRequestInputTokens(req.ChatRequest)
	require.Positive(t, count)

	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.Nil(t, shortCircuit)

	raw, err := json.Marshal(fake.lastBody)
	require.NoError(t, err)
	wire := string(raw)

	assert.NotContains(t, wire, "estimate",
		"no estimate-shaped entry rides the reserve: the kinds are mechanisms, closed sets both sides")
	assert.NotContains(t, wire, "\"conservative\"",
		"the conservative count is a rejection aid; it is not a kind, a ceiling or a price")
	// The hold is the deployment's configured ceiling in credits, never something the input
	// count computed.
	assert.InDelta(t, 100.0, fake.lastBody["maximumCredits"], 0,
		"the hold ceiling stays the configured one; the count prices nothing")
	// The input evidence names the configured ceiling even though the count is smaller: what
	// the reserve carries is the bound that is ENFORCED, not the number that was measured.
	evidence := fake.lastBody["boundEvidence"].([]any)
	assert.Contains(t, evidence, map[string]any{"kind": "self_enforced_input_truncation", "maximumTokens": float64(boundTestInputCeiling)})
	assert.NotContains(t, evidence, map[string]any{"kind": "self_enforced_input_truncation", "maximumTokens": float64(count)},
		"the measured count must not masquerade as the enforced ceiling")
}

// --- the wire contract, derived per request --------------------------------------------------

func TestBoundEvidenceDerivedPerRequestReplacesConfiguredEvidence(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithInputBound(fake.endpoint(), boundTestInputCeiling))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, boundWellFormedRequest())
	require.NoError(t, err)
	require.Nil(t, shortCircuit)

	_, body := fake.snapshot()
	assert.Equal(t, []any{
		map[string]any{"kind": "self_enforced_input_truncation", "maximumTokens": float64(boundTestInputCeiling)},
		map[string]any{"kind": "provider_max_tokens", "maximumTokens": float64(boundTestMaxTokens)},
	}, body["boundEvidence"],
		"the reserve carries exactly the two per-request proofs: the enforced input ceiling and the request's own output cap")
}

// The deployment cannot even wire a door that would send configured evidence: a reserve
// without an input ceiling is an incomplete door and is refused as a wiring fault before any
// request is served through it.
func TestBoundEvidenceReserveWithoutInputCeilingIsAWiringFault(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithInputBound(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, plugin, ctx, boundWellFormedRequest())

	require.NotNil(t, bifrostErr.Error.Code)
	assert.Equal(t, CreditRefusalAdmissionUnreachable, *bifrostErr.Error.Code,
		"a door without an input ceiling is misassembled machinery, not this call's evidence")
	assert.Equal(t, 0, fake.calls())
}
