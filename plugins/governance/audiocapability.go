package governance

// The audio path capability gate (Bifrost 14.4, #313): the deployment's record of which
// audio processing paths are VERIFIED as billable — carried per (path, stream) entrance the
// way OutputCapEvidence is carried per model × call-method cell — and the admission-funnel
// consult that refuses an unverified audio path before the reserve door is ever asked.
//
// THE RECORD. 14.3's registry (OpenBot's audio-capability-evidence) enumerates the audio
// paths the gateway actually carries and the evidence each needs; what lands here is the
// deployment's verification of one path: the environment it ran in, the result it
// concluded in, and — when the result is not "verified" — the MISSING ITEMS that hold the
// path unverified, in the registry's own closed code set. The structure is closed on
// purpose: an unknown result or an unknown missing-item code is not evidence, and a record
// missing its verification facts is not evidence either — each refuses its path with the
// reason visible, the way every other fail-closed gate in this plugin refuses.
//
// THE GATE. evaluateCreditAdmission consults the record for the request's audio entrance
// (speech and transcription, each with a stream variant — the four typed audio request
// types) BEFORE the free/unknown pricing classification and before the reserve door: an
// unverified path is not callable at all, whatever the model's price says, and the refusal
// names the specific missing evidence items so an operator fixes the right thing. The
// door is never asked to hold for a path no evidence verifies — so the provider-side proof
// (供应商调用数为 0) is structural: the refusal is a pre-hook short circuit, no fallback is
// tried, and the rejected mark keeps the post hooks' accounting from writing anything.
//
// WHAT THIS MODULE DELIBERATELY DOES NOT DO. It does not verify anything itself, and it
// does not carry pricing: verification happens against real providers, by people, in
// reproducible environments, and the per-basis price half of a path's usability lives in
// OpenBot's price snapshots (03.2/03.3), reached through the reserve door. A path whose
// record is missing stays unverified and refused - never invented (Q11).

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// The closed result vocabulary. Verified is the only enabling answer; the other two are
// recorded states that keep a path unavailable with its reason visible.
const (
	// AudioCapabilityEvidenceVerified: the path's billing evidence was verified in the
	// named environment and is reproducible from the named source.
	AudioCapabilityEvidenceVerified = "verified"
	// AudioCapabilityEvidenceNotEnforced: verification ran and concluded the path's
	// billing cannot be enforced (e.g. the meter does not see what the provider bills).
	AudioCapabilityEvidenceNotEnforced = "not_enforced"
	// AudioCapabilityEvidenceUnverified: no verification could be performed; the path
	// stays unavailable.
	AudioCapabilityEvidenceUnverified = "unverified"
)

// The closed missing-item code set — the same codes OpenBot's 14.3 registry carries, so a
// refusal here and a gap report there name the same facts.
const (
	AudioCapabilityMissingNoWirePriceSource   = "no_wire_price_source"
	AudioCapabilityMissingNoInputMeterWired   = "no_input_meter_wired"
	AudioCapabilityMissingNoAdmissionWiring   = "no_admission_wiring"
	AudioCapabilityMissingNoGatewayEntrypoint = "no_gateway_entrypoint"
)

// audioCapabilityMissingItemCodes is the closed set a record's missing items may carry.
var audioCapabilityMissingItemCodes = map[string]bool{
	AudioCapabilityMissingNoWirePriceSource:   true,
	AudioCapabilityMissingNoInputMeterWired:   true,
	AudioCapabilityMissingNoAdmissionWiring:   true,
	AudioCapabilityMissingNoGatewayEntrypoint: true,
}

// audioCapabilityEvidenceResults is the closed result set a record may carry.
var audioCapabilityEvidenceResults = map[string]bool{
	AudioCapabilityEvidenceVerified:    true,
	AudioCapabilityEvidenceNotEnforced: true,
	AudioCapabilityEvidenceUnverified:  true,
}

// AudioCapabilityMissingItem is one piece of evidence a path lacks, named in the registry's
// closed code set with the human sentence that says what it means.
type AudioCapabilityMissingItem struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// AudioCapabilityRecord is one audio entrance's capability evidence: the (path, stream)
// pair that identifies the entrance the way 14.3's registry names it, where the
// verification ran, what it concluded, and what is missing when it did not conclude
// "verified".
type AudioCapabilityRecord struct {
	// Path the entrance's gateway path, e.g. "/v1/audio/speech".
	Path string `json:"path"`
	// Stream: the entrance's stream variant — part of its identity, with the path.
	Stream bool `json:"stream"`
	// Environment where the verification ran, e.g. "fake-provider-harness". A claim of
	// verification without a place it happened is not verification.
	Environment string `json:"environment"`
	// Result the closed verification result; only "verified" enables the path.
	Result string `json:"result"`
	// MissingItems the evidence gaps that hold the path unverified, each in the closed
	// code set; required whenever the result is not "verified".
	MissingItems []AudioCapabilityMissingItem `json:"missingItems,omitempty"`
	// VerifiedAt when the verification concluded (RFC 3339).
	VerifiedAt string `json:"verifiedAt"`
	// Source what reproduces the verification.
	Source string `json:"source"`
}

// evidenceProblem names the first way the record fails to be usable evidence, or "" when it
// is complete and in vocabulary. A record that fails here is not quietly skipped - the gate
// refuses its path with this problem shown, so malformed evidence is as visible as absent
// evidence.
func (r AudioCapabilityRecord) evidenceProblem() string {
	switch {
	case strings.TrimSpace(r.Path) == "":
		return "the record names no path"
	case strings.TrimSpace(r.Environment) == "":
		return "the record names no verification environment"
	case !audioCapabilityEvidenceResults[r.Result]:
		return fmt.Sprintf("the record claims unknown result %q; the closed set is %q, %q, %q",
			r.Result, AudioCapabilityEvidenceVerified, AudioCapabilityEvidenceNotEnforced, AudioCapabilityEvidenceUnverified)
	case r.Result != AudioCapabilityEvidenceVerified && len(r.MissingItems) == 0:
		return fmt.Sprintf("a %q record carries no missing items: what holds the path unverified must be named", r.Result)
	case strings.TrimSpace(r.VerifiedAt) == "":
		return "the record names no verification time"
	case strings.TrimSpace(r.Source) == "":
		return "the record names no reproducing source"
	}
	for _, item := range r.MissingItems {
		if !audioCapabilityMissingItemCodes[item.Code] {
			return fmt.Sprintf("the record's missing item claims unknown code %q", item.Code)
		}
		if strings.TrimSpace(item.Detail) == "" {
			return fmt.Sprintf("the record's missing item %q carries no detail", item.Code)
		}
	}
	return ""
}

// audioRequestEntrance maps a typed audio request type onto the (path, stream) entrance the
// registry names. The bool answers whether the request is an audio entrance at all; every
// other request type is not this gate's question and costs exactly this switch.
func audioRequestEntrance(requestType schemas.RequestType) (path string, stream bool, isAudio bool) {
	switch requestType {
	case schemas.SpeechRequest:
		return "/v1/audio/speech", false, true
	case schemas.SpeechStreamRequest:
		return "/v1/audio/speech", true, true
	case schemas.TranscriptionRequest:
		return "/v1/audio/transcriptions", false, true
	case schemas.TranscriptionStreamRequest:
		return "/v1/audio/transcriptions", true, true
	default:
		return "", false, false
	}
}

// missingItemSentence renders one missing item for a refusal's message.
func missingItemSentence(item AudioCapabilityMissingItem) string {
	if strings.TrimSpace(item.Detail) == "" {
		return item.Code
	}
	return fmt.Sprintf("%s (%s)", item.Code, item.Detail)
}

// audioCapabilityProblem answers the gate for one request: "" when the request is not an
// audio entrance (not this gate's question) or a VERIFIED record covers its entrance;
// otherwise the sentence that refuses it, naming the specific missing evidence items.
//
// Absence is refusal, both directions: an entrance with no record at all is refused as
// unverified with every item named missing (nothing was verified), and a record whose shape
// fails its own evidence check is refused with that problem — malformed evidence is never
// dressed up as enablement. The lookup is linear over the configured records: a deployment
// carries a handful of audio entrances, and the common case (non-audio request types)
// returns before any of this runs.
func (p *GovernancePlugin) audioCapabilityProblem(requestType schemas.RequestType) string {
	path, stream, isAudio := audioRequestEntrance(requestType)
	if !isAudio {
		return ""
	}
	p.cfgMutex.RLock()
	var records []AudioCapabilityRecord
	if p.openBotCreditAdmission != nil {
		records = p.openBotCreditAdmission.AudioCapabilityEvidence
	}
	p.cfgMutex.RUnlock()

	var matched *AudioCapabilityRecord
	for i := range records {
		if records[i].Path == path && records[i].Stream == stream {
			matched = &records[i]
			break
		}
	}
	if matched == nil {
		return fmt.Sprintf("the audio path %q (stream=%t) has no capability evidence record: the path stays unverified and no provider is called",
			path, stream)
	}
	if problem := matched.evidenceProblem(); problem != "" {
		return fmt.Sprintf("the audio path %q (stream=%t) carries malformed evidence (%s): the path stays unverified and no provider is called",
			path, stream, problem)
	}
	if matched.Result == AudioCapabilityEvidenceVerified {
		return ""
	}
	items := make([]string, 0, len(matched.MissingItems))
	for _, item := range matched.MissingItems {
		items = append(items, missingItemSentence(item))
	}
	return fmt.Sprintf("the audio path %q (stream=%t) is not verified (%s in %s at %s): missing evidence — %s; no provider is called",
		path, stream, matched.Result, matched.Environment, matched.VerifiedAt, strings.Join(items, "; "))
}
