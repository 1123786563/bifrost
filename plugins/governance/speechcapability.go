package governance

// The speech tail-constraint gate (Bifrost 13.4, #309): which speech call modes can have
// their tail BOUNDED — the generated amount determinable by a meter the server itself
// reads, and the billing stoppable at the caller-attested cap — and which stay refused
// with the reason named, BEFORE the pricing classification and before the reserve door is
// ever asked. A path whose tail cannot be constrained is a systematic overspend source,
// and the ticket's own stance is 宁可少开放一个路径，也不开放无法约束尾部的路径.
//
// THE JUDGMENT MIRRORS OPENBOT'S speech-tail-availability.ts. Both faces stand on the same
// structural fact: only the WAV container carries duration metadata this gateway's own
// meters read (the `fmt ` byte rate against the `data ` size — 14.1's whole-file parser
// and 13.3's chunk-wise interruption collector); mp3, opus, aac, flac and pcm have no
// server-trusted duration meter, so their generated amount cannot be determined and the
// attested cap cannot stop their billing (sever-at-cap and the confirmed-tail settle both
// need the meter). The wire's own default is the honest trap: an absent response_format
// is mp3 by the wire's definition, so the DEFAULT synthesis call is tail-unconstrainable
// and refuses here — the refusal says the default substitution, never a silent pass.
//
// THE GATE IS FAIL-CLOSED IN BOTH DIRECTIONS. A speech request whose typed body the funnel
// does not carry (no SpeechRequest on the evaluation request) is refused as the mp3
// default too: a call whose format the gateway cannot read is a call whose tail it cannot
// bound, and absence of evidence is never enablement (Q11). The refusal is a pre-hook
// short circuit — the reserve door records zero calls and the rejected mark keeps the
// post hooks' accounting from writing anything, so the provider-side proof (供应商调用数
// 为 0) is structural.
//
// WHAT THIS MODULE DELIBERATELY DOES NOT DO. It does not price anything (the reserve door
// and the locked basis own that), it does not verify providers (OpenBot's 14.3 registry
// and the deployment's audio capability records own that, consulted by the neighbouring
// gate), and it does not meter audio (the meters live in OpenBot's pricing boundary). It
// answers one question per request: can this call mode's tail be bounded at all.

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// SpeechTailConstrainableFormatWav is the only wire format whose audio this gateway's own
// meters can read — the one format the tail constraint can bound.
const SpeechTailConstrainableFormatWav = "wav"

// SpeechTailDefaultResponseFormat is what the wire itself substitutes for an absent
// response_format on /v1/audio/speech (SpeechParameters: "Default is mp3").
const SpeechTailDefaultResponseFormat = "mp3"

// speechWireResponseFormats is the wire's closed response-format set. A format outside it
// refuses with itself named — not a pass, and not silently remapped.
var speechWireResponseFormats = map[string]bool{
	"mp3":  true,
	"opus": true,
	"aac":  true,
	"flac": true,
	"wav":  true,
	"pcm":  true,
}

// speechRequestResponseFormat reads the request's own response_format; "" when the typed
// speech body or its params are not carried. The caller substitutes the wire default.
func speechRequestResponseFormat(evaluationRequest *EvaluationRequest) string {
	if evaluationRequest == nil || evaluationRequest.LLMRequest == nil {
		return ""
	}
	speech := evaluationRequest.LLMRequest.SpeechRequest
	if speech == nil || speech.Params == nil {
		return ""
	}
	return strings.TrimSpace(speech.Params.ResponseFormat)
}

// speechTailProblem answers the gate for one request: "" when the request is not a speech
// entrance (not this gate's question) or its response format is constrainable; otherwise
// the sentence that refuses it — the format, both reasons it cannot be bounded, and the
// fact that no provider is called. Deterministic and pure: the same format always answers
// the same sentence, so this gate and OpenBot's speech-tail-availability.ts (which the
// admission door and the discovery interface consult) refuse identically or the pair is a
// defect.
func (p *GovernancePlugin) speechTailProblem(evaluationRequest *EvaluationRequest) string {
	if evaluationRequest == nil {
		return ""
	}
	var stream bool
	switch evaluationRequest.RequestType {
	case schemas.SpeechRequest:
		stream = false
	case schemas.SpeechStreamRequest:
		stream = true
	default:
		return ""
	}
	raw := speechRequestResponseFormat(evaluationRequest)
	format := raw
	if format == "" {
		format = SpeechTailDefaultResponseFormat
	}
	if format == SpeechTailConstrainableFormatWav {
		return ""
	}
	naming := fmt.Sprintf("no server-readable duration meter exists for %s audio on this gateway", format)
	if !speechWireResponseFormats[format] {
		naming = fmt.Sprintf("%s is not even in the wire's closed format set, so no meter could exist", format)
	}
	return fmt.Sprintf(
		"speech_tail_unconstrainable: the speech path \"/v1/audio/speech\" (stream=%t, response_format=%q%s) cannot have its tail constrained: %s; the attested duration cap cannot stop the billing of audio the server cannot measure; the path stays unenabled and no provider is called",
		stream, format,
		map[bool]string{true: ", the wire default for an absent format", false: ""}[raw == ""],
		naming,
	)
}
