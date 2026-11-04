package governance

// The per-request bound evidence (07.6): the proof a reserve carries that THIS call's cost
// ceiling can be enforced, derived from the request the funnel is about to dispatch.
//
// WHY PER REQUEST. Until now the reserve's bound evidence rode deployment configuration -
// a list of mechanism kinds and ceilings the same for every call. A config list cannot
// prove anything about the call it rides with: the input bound is only true if THIS
// request's whole billable input was counted and brought under a ceiling, and the output
// bound is only true if THIS request carries a max_tokens the provider enforces. So the
// evidence is constructed here, per attempt, from the request itself, and the deployment
// contributes the one parameter the mechanism needs: the input ceiling.
//
// THE INPUT SIDE (ADR 0001). The counter prices the complete serialized request - messages,
// the system prompt, tool definitions, multimodal attachments, and the provider-specific
// extra params the adapters forward verbatim - at the conservative calibration: a floor of
// 2 bytes per token times a 1.25 safety factor, plus a per-message overhead. Serialization
// is the completeness argument: whatever the request carries is in the bytes, so a shape
// the counter cannot serialize is a shape it cannot bound, and uncountable is unbounded
// (fail-closed, never zero). Self-enforcement is the truncation the fitter applies before
// the reserve is asked: the oldest non-system messages are dropped - the system block and
// the latest message always stay - until the count fits the configured ceiling, and a call
// that cannot fit is refused instead of sent. What dispatches is the request that was
// counted. The count itself is a rejection aid only: it never rides the reserve, never
// prices anything, and no estimate-shaped entry can appear in the evidence, whose kinds
// are the closed mechanism vocabulary both sides of the reserve door share.
//
// THE OUTPUT SIDE. A call admits only with an output cap the request itself carries - the
// max_tokens (or its modern and legacy spellings) the provider is contractually bound to
// enforce. A call that caps nothing has an unbounded generation side, and "the model
// usually stops" is not a ceiling; it is refused before the reserve door is asked.
//
// Insufficiency of either side refuses the call method (Q11: only model × call-method cells
// whose cost ceiling can be enforced are enabled), with the same short-circuit shape as
// every other credit refusal and a reason that names the model and a caller-actionable
// cause - never the deployment's credentials, accounts, or what the billing service said.

import (
	"encoding/json"
	"math"

	"github.com/maximhq/bifrost/core/schemas"
)

// The conservative calibration (ADR 0001; input-bound.ts carries the same constants on the
// OpenBot side). The floor and factor sit ABOVE a typical tokenizer's answer for the same
// bytes - the conservative direction, where a misestimate overcounts admission and never
// undercounts a dispatch.
const (
	conservativeBytesPerTokenFloor       = 2.0
	conservativeSafetyFactor             = 1.25
	conservativePerMessageOverheadTokens = 4
)

// countChatRequestInputTokens is the conservative count of one chat request's complete
// billable input: every byte the typed request serializes to, plus the extra params the
// adapters forward verbatim (they carry json:"-"; a count that skipped them would miss
// exactly the parameters a caller chose to bill through). An input that cannot be fully
// serialized counts as unbounded - the admission reads that as evidence insufficiency.
func countChatRequestInputTokens(req *schemas.BifrostChatRequest) int64 {
	if req == nil {
		return math.MaxInt64
	}
	size, ok := serializedBillableBytes(req)
	if !ok {
		return math.MaxInt64
	}
	if req.Params != nil {
		extra, ok := serializedBillableBytes(req.Params.ExtraParams)
		if !ok {
			return math.MaxInt64
		}
		size += extra
	}
	return conservativeTokens(size, len(req.Input))
}

// serializedBillableBytes serializes each part and sums the UTF-8 byte lengths. A part that
// refuses to serialize - a cyclic structure, an unencodable value - fails the whole count:
// ok=false, which every caller treats as "cannot count in full", never as zero.
func serializedBillableBytes(parts ...any) (int, bool) {
	total := 0
	for _, part := range parts {
		encoded, err := json.Marshal(part)
		if err != nil {
			return 0, false
		}
		total += len(encoded)
	}
	return total, true
}

// conservativeTokens applies the ADR 0001 calibration to a byte size and message count.
// The result saturates at MaxInt64 rather than overflowing: saturation reads as unbounded,
// which is the only safe reading of a number too large to hold.
func conservativeTokens(size, messages int) int64 {
	counted := int64(math.Ceil(float64(size)/conservativeBytesPerTokenFloor*conservativeSafetyFactor)) +
		int64(messages)*conservativePerMessageOverheadTokens
	if counted < 0 {
		return math.MaxInt64
	}
	return counted
}

// chatOutputCapTokens is the output cap the request itself carries and the provider
// enforces: max_completion_tokens, or the legacy max_tokens some callers still send in the
// extra params. Zero means the call caps nothing - which is evidence insufficiency, not a
// cap of zero: an uncapped generation is an unbounded cost, and no hold prices infinity.
func chatOutputCapTokens(params *schemas.ChatParameters) int64 {
	if params == nil {
		return 0
	}
	if params.MaxCompletionTokens != nil && *params.MaxCompletionTokens > 0 {
		return int64(*params.MaxCompletionTokens)
	}
	return positiveExtraParam(params.ExtraParams, "max_tokens")
}

// positiveExtraParam reads one positive integer out of the extra params, however the JSON
// decoder or a programmatic caller spelled it.
func positiveExtraParam(extra map[string]any, key string) int64 {
	switch value := extra[key].(type) {
	case float64:
		if value > 0 {
			return int64(value)
		}
	case int:
		if value > 0 {
			return int64(value)
		}
	case int64:
		if value > 0 {
			return value
		}
	case json.Number:
		if n, err := value.Int64(); err == nil && n > 0 {
			return n
		}
	}
	return 0
}

// fitChatInput is the self-enforced input bound (ADR 0001, spec decision 1b/3): drop the
// oldest non-system messages until the conservative count fits the ceiling; the leading
// system block and the latest message always stay. The returned slice is what the request
// should dispatch. ok=false - including when nothing is droppable, or the system+latest
// pair alone cannot fit - means the call has no self-enforced bound and is refused, never
// sent over the ceiling.
func fitChatInput(req *schemas.BifrostChatRequest, ceiling int64) ([]schemas.ChatMessage, bool) {
	input := req.Input
	if len(input) <= 2 {
		return input, countChatRequestInputTokens(req) <= ceiling
	}
	head := leadingSystemCount(input)
	last := len(input) - 1
	for drop := 1; head+drop <= last; drop++ {
		candidate := make([]schemas.ChatMessage, 0, len(input)-drop)
		candidate = append(candidate, input[:head]...)
		candidate = append(candidate, input[head+drop:]...)
		fitted := *req
		fitted.Input = candidate
		if countChatRequestInputTokens(&fitted) <= ceiling {
			return candidate, true
		}
	}
	return req.Input, false
}

// leadingSystemCount counts the run of system-role messages at the front of the input; the
// fitter never drops them, so the system prompt survives every truncation.
func leadingSystemCount(input []schemas.ChatMessage) int {
	count := 0
	for _, message := range input {
		if message.Role == schemas.ChatMessageRoleSystem {
			count++
			continue
		}
		break
	}
	return count
}

// deriveChatBoundEvidence constructs one chat call's reserve evidence: the enforced input
// ceiling and the request's own output cap, in that order. The request is left carrying the
// truncated input, so the request that dispatches is exactly the request that was counted.
func deriveChatBoundEvidence(req *schemas.BifrostChatRequest, reserve *CreditReserveConfig) ([]CreditReserveBoundEvidence, string) {
	if req == nil {
		return nil, "the request carries no chat input to count"
	}
	outputCap := chatOutputCapTokens(req.Params)
	if outputCap == 0 {
		return nil, "the call carries no provider-enforced output cap (set max_tokens)"
	}
	fitted, ok := fitChatInput(req, reserve.InputCeilingTokens)
	if !ok {
		return nil, "the call's input exceeds the deployment's input ceiling even after truncating to the system block and the latest message"
	}
	req.Input = fitted
	evidence := []CreditReserveBoundEvidence{
		{Kind: "self_enforced_input_truncation", MaximumTokens: reserve.InputCeilingTokens},
		{Kind: "provider_max_tokens", MaximumTokens: outputCap},
	}
	for _, entry := range evidence {
		if !creditReserveBoundEvidenceKinds[entry.Kind] || entry.MaximumTokens <= 0 {
			return nil, "the derived bound evidence is not a closed-vocabulary mechanism with a positive ceiling"
		}
	}
	return evidence, ""
}

// countEmbeddingRequestInputTokens is the conservative count of one embeddings request's
// complete billable input: every byte the typed request serializes to - the model name,
// the request-level params, and EVERY batch entry's full content - plus the extra params
// the adapters forward verbatim, at the same ADR 0001 calibration the chat counter uses,
// with the per-entry overhead charged per ENTRY (each entry is the smallest unit a provider
// could bill separately). The count is of what will be sent, as it will be sent.
func countEmbeddingRequestInputTokens(req *schemas.BifrostEmbeddingRequest) int64 {
	if req == nil {
		return math.MaxInt64
	}
	size, ok := serializedBillableBytes(req)
	if !ok {
		return math.MaxInt64
	}
	if req.Params != nil {
		extra, ok := serializedBillableBytes(req.Params.ExtraParams)
		if !ok {
			return math.MaxInt64
		}
		size += extra
	}
	return conservativeTokens(size, len(req.Input))
}

// deriveEmbeddingBoundEvidence constructs one embeddings call's reserve evidence (11.2, on
// 11.1's boundary): the counted-in-full input under the deployment's ceiling, and NO output
// entry - the vector output has no billable dimension on the wire (the response's usage
// carries prompt tokens only), so there is no output cap to attest. Unlike chat there is no
// truncation fitter: an embeddings request's entries ARE the payload - dropping one changes
// the answer the caller asked for - so the input bound is enforced by REFUSAL, never by
// sending something uncounted or short of what was asked.
func deriveEmbeddingBoundEvidence(req *schemas.BifrostEmbeddingRequest, reserve *CreditReserveConfig) ([]CreditReserveBoundEvidence, string) {
	if req == nil {
		return nil, "the request carries no embedding input to count"
	}
	counted := countEmbeddingRequestInputTokens(req)
	if counted > reserve.InputCeilingTokens {
		return nil, "the embeddings input exceeds the deployment's input ceiling; batch entries are the payload, so the call is refused rather than sent uncounted"
	}
	evidence := []CreditReserveBoundEvidence{
		{Kind: "self_enforced_input_refusal", MaximumTokens: reserve.InputCeilingTokens},
	}
	for _, entry := range evidence {
		if !creditReserveBoundEvidenceKinds[entry.Kind] || entry.MaximumTokens <= 0 {
			return nil, "the derived bound evidence is not a closed-vocabulary mechanism with a positive ceiling"
		}
	}
	return evidence, ""
}

// deriveBoundEvidence builds the reserve's bound evidence for one dispatchable request:
// the complete serialized input, self-enforced under the deployment's input ceiling, plus
// the output cap the request carries. problem != "" means this call method lacks provable,
// enforceable bounds and is refused with cap_not_provable - estimates were only ever
// allowed to cause this refusal, never to prevent it.
//
// The evidence covers the call shapes this integration dispatches today: chat (truncation
// + output cap) and embeddings (count-and-refuse input; the vector output needs no cap).
// Every other call type - and every request whose body would go to the provider raw, which
// the gateway can neither count in full nor truncate - stays refused until its own evidence
// exists: interface existence is not enablement evidence (Q11/Q20).
func deriveBoundEvidence(ctx *schemas.BifrostContext, evaluationRequest *EvaluationRequest, reserve *CreditReserveConfig) ([]CreditReserveBoundEvidence, string) {
	req := evaluationRequest.LLMRequest
	if req == nil {
		return nil, "this call type has no complete serialized input the gateway can count"
	}
	if useRawBody, ok := ctx.Value(schemas.BifrostContextKeyUseRawRequestBody).(bool); ok && useRawBody {
		return nil, "the call dispatches a raw request body the gateway can neither count in full nor truncate"
	}
	switch evaluationRequest.RequestType {
	case schemas.ChatCompletionRequest, schemas.ChatCompletionStreamRequest:
		return deriveChatBoundEvidence(req.ChatRequest, reserve)
	case schemas.EmbeddingRequest:
		return deriveEmbeddingBoundEvidence(req.EmbeddingRequest, reserve)
	default:
		return nil, "this call type has no provable cost-ceiling evidence yet"
	}
}
