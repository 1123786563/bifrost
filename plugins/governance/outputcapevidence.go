package governance

// The per-model output-cap enforcement evidence (07.7): the deployment's record that a
// provider ACTUALLY enforces the output cap a request carries, kept per model × call-method
// cell, and the two places that record is held against: the admission funnel, which refuses
// a cell no verification ever concluded for, and the booking path, which never bills beyond
// the cap a held reservation attested.
//
// THE RECORD. 07.6 derives the per-request half of the output bound: the call must carry a
// max_tokens the provider is contractually bound to enforce. A contract is still a paper
// claim, so the deployment also records, per cell, the verification it actually performed -
// against which environment, concluding in what result, reproducible from what source. The
// structure is closed on purpose: an unknown mechanism is not evidence, a result the
// vocabulary never carried is not evidence, and a record missing any verification fact is
// not evidence - each refuses its cell with the reason visible, the way every other
// fail-closed gate in this plugin refuses. Unverified and not-enforced cells are recorded
// TOO, with their reason: "we could not verify this" must stay visible and distinct from
// "we never looked", and neither is ever dressed up as enablement (Q11: only cells whose
// cost ceiling can be enforced are enabled; interface existence is not evidence).
//
// THE GATE. reserveBeforeDispatch consults the record for the attempt's cell before the
// reserve door is asked: the door is never asked to hold for a call whose ceiling the
// provider has not been shown to enforce, on this attempt or any fallback after it.
//
// THE EXCESS RULE. Even a verified cell is defended in depth where cost is booked: the post
// hook clamps billable output usage to the cap the reservation carried. A provider that
// reports generation beyond the cap has produced cost no reservation covers; the covered
// part settles, the violation is logged, and no unreserved cost enters the accounting -
// clamping can under-charge a misbehaving provider, which is a bounded, visible loss, where
// booking the excess would be an unbounded, invisible one (ADR 0003).
//
// What this module deliberately does not do: verify anything itself. Verification happens
// against real providers, by people, in environments that can reproduce it; what lands here
// is the record of that work. Missing credentials or environment means the cell stays
// unverified and refused - never invented.

import (
	"fmt"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
)

// OutputCapMechanismProviderMaxTokens is the one mechanism the closed vocabulary carries:
// the max_tokens (or max_completion_tokens) the request itself declares and the provider is
// contractually bound to enforce - the same kind the per-request evidence (07.6) attests. A
// record claiming any other mechanism is not evidence.
const OutputCapMechanismProviderMaxTokens = "provider_max_tokens"

// The closed result vocabulary. Verified is the only enabling answer; the other two are
// recorded states that keep a cell unavailable with its reason visible.
const (
	// OutputCapEvidenceVerified: enforcement of the output cap was verified in the named
	// environment and is reproducible from the named source.
	OutputCapEvidenceVerified = "verified"
	// OutputCapEvidenceNotEnforced: verification ran and concluded the provider does NOT
	// honor the cap for this cell.
	OutputCapEvidenceNotEnforced = "not_enforced"
	// OutputCapEvidenceUnverified: no verification could be performed (e.g. no real
	// provider credentials or environment available); the cell stays unavailable.
	OutputCapEvidenceUnverified = "unverified"
)

// OutputCapEnforcementRecord is one cell's structured enforcement evidence (AC06): the
// model × call-method cell it is about, the mechanism the cap rides, the environment the
// verification ran in, the result it concluded in, and what reproduces it. The whole record
// is what 22.3 conformance aggregates; every field is required exactly so that an export
// can be read as evidence rather than as a summary of one.
type OutputCapEnforcementRecord struct {
	// Provider the cell's provider, e.g. "openai".
	Provider string `json:"provider"`
	// Model the cell's model, as the catalog names it.
	Model string `json:"model"`
	// CallMethod the cell's call method, in the credit protocol's closed capability
	// vocabulary ("chat", "embedding", ...), so a cell is enabled per call method, never
	// for a model as a whole.
	CallMethod string `json:"callMethod"`
	// Mechanism how the cap is enforced; the closed vocabulary above.
	Mechanism string `json:"mechanism"`
	// Environment where the verification ran, e.g. "openai-production-2026-10" or
	// "fake-provider-harness". A claim of verification without a place it happened is not
	// verification.
	Environment string `json:"environment"`
	// Result the closed verification result; only "verified" enables the cell.
	Result string `json:"result"`
	// Detail the recorded reason, required whenever the result is not "verified": the
	// cause a refused caller is shown.
	Detail string `json:"detail,omitempty"`
	// VerifiedAt when the verification concluded (RFC 3339), part of what makes the
	// evidence reproducible and auditable.
	VerifiedAt string `json:"verifiedAt"`
	// Source what reproduces the verification: the harness, command or fixture that
	// produced the recorded result.
	Source string `json:"source"`
}

// outputCapEvidenceResults is the closed result set a record may carry.
var outputCapEvidenceResults = map[string]bool{
	OutputCapEvidenceVerified:    true,
	OutputCapEvidenceNotEnforced: true,
	OutputCapEvidenceUnverified:  true,
}

// evidenceProblem names the first way the record fails to be usable evidence, or "" when it
// is complete and in vocabulary. A record that fails here is not quietly skipped - the gate
// refuses its cell with this problem shown, so malformed evidence is as visible as absent
// evidence.
func (r OutputCapEnforcementRecord) evidenceProblem() string {
	switch {
	case strings.TrimSpace(r.Provider) == "":
		return "the record names no provider"
	case strings.TrimSpace(r.Model) == "":
		return "the record names no model"
	case strings.TrimSpace(r.CallMethod) == "":
		return "the record names no call method"
	case r.Mechanism != OutputCapMechanismProviderMaxTokens:
		return fmt.Sprintf("the record claims unknown enforcement mechanism %q; the only enforced output mechanism is %q", r.Mechanism, OutputCapMechanismProviderMaxTokens)
	case strings.TrimSpace(r.Environment) == "":
		return "the record names no verification environment"
	case !outputCapEvidenceResults[r.Result]:
		return fmt.Sprintf("the record carries unknown verification result %q", r.Result)
	case r.Result != OutputCapEvidenceVerified && strings.TrimSpace(r.Detail) == "":
		return "a non-verified record carries no recorded reason"
	case strings.TrimSpace(r.VerifiedAt) == "":
		return "the record carries no verification timestamp"
	case strings.TrimSpace(r.Source) == "":
		return "the record names no reproducible source"
	}
	return ""
}

// outputCapEnforcementProblem is the model-level half of the reserve door: for THIS attempt's
// cell it names why the call may not dispatch for lack of enforced-output evidence, or "" when
// the deployment holds a verified record for it. The check is fail-closed in every direction:
// no record, an unusable record, a record that concluded against enforcement, and a call
// method the capability vocabulary cannot map all refuse - absence is never enablement.
func (p *GovernancePlugin) outputCapEnforcementProblem(provider schemas.ModelProvider, model string, requestType schemas.RequestType) string {
	callMethod := creditCapabilityForRequestType(requestType)
	if callMethod == "unknown" {
		return "this call method has no boundable output the gateway can attest, so no output-cap enforcement evidence can exist for it"
	}
	p.cfgMutex.RLock()
	var records []OutputCapEnforcementRecord
	if p.openBotCreditAdmission != nil {
		records = p.openBotCreditAdmission.OutputCapEvidence
	}
	p.cfgMutex.RUnlock()

	var matches []OutputCapEnforcementRecord
	for _, record := range records {
		if record.Provider == string(provider) && record.Model == model && record.CallMethod == callMethod {
			matches = append(matches, record)
		}
	}
	if len(matches) == 0 {
		return "no output-cap enforcement evidence is recorded for this model and call method: the deployment has not verified that the provider enforces the model's output cap, so the model is not enabled"
	}
	for _, record := range matches {
		if problem := record.evidenceProblem(); problem != "" {
			return fmt.Sprintf("the output-cap enforcement evidence recorded for this model and call method is not usable (%s), so the model is not enabled", problem)
		}
	}
	for _, record := range matches {
		if record.Result != OutputCapEvidenceVerified {
			return fmt.Sprintf("the provider's enforcement of this model's output cap is %s, verified in environment %q (%s), so the model is not enabled", record.Result, record.Environment, record.Detail)
		}
	}
	return ""
}

// OutputCapEvidenceSnapshot exports the enforcement evidence whole, as recorded: the input
// 22.3 conformance aggregates to answer "which cells are enabled, on what verification".
// The records are copied, so a caller holding the snapshot sees the configuration that was
// live at export time and cannot mutate the gate through it.
func (p *GovernancePlugin) OutputCapEvidenceSnapshot() []OutputCapEnforcementRecord {
	p.cfgMutex.RLock()
	defer p.cfgMutex.RUnlock()
	if p.openBotCreditAdmission == nil {
		return nil
	}
	out := make([]OutputCapEnforcementRecord, len(p.openBotCreditAdmission.OutputCapEvidence))
	copy(out, p.openBotCreditAdmission.OutputCapEvidence)
	return out
}

// clampChatUsageToOutputCap holds billable usage to the cap the reservation attested: when
// the provider reports output beyond the cap, the returned copy carries the capped output -
// and no provider-computed cost figure, which is part of the same violation and is
// recomputed from the capped tokens instead - and violated reports that the response broke
// the cap. usage nil, no cap, or output within the cap returns (nil, false): nothing to
// change, the caller books the response as it stands. The original response is never
// mutated: what the provider reported stays on the response; only what is BOOKED is held.
func clampChatUsageToOutputCap(usage *schemas.BifrostLLMUsage, outputCapTokens int64) (*schemas.BifrostLLMUsage, bool) {
	if usage == nil || outputCapTokens <= 0 || int64(usage.CompletionTokens) <= outputCapTokens {
		return nil, false
	}
	clamped := *usage
	clamped.Cost = nil
	clamped.CompletionTokens = int(outputCapTokens)
	// Keep every non-output component of the total (cache, audio and whatever else the
	// provider folded in), dropping exactly the excess output; the floor guards a total
	// that was already inconsistent with its own parts.
	remaining := usage.TotalTokens - (usage.CompletionTokens - int(outputCapTokens))
	if remaining < usage.PromptTokens {
		remaining = usage.PromptTokens
	}
	clamped.TotalTokens = remaining
	return &clamped, true
}
