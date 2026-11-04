package governance

// Tool admission (Bifrost 20.2, #342): a tool-call kind the deployment cannot fully price
// AND cap is not enabled. Under the strict-billing regime (the OpenBot credit-admission
// extension configured), a tool call that would spend money no one priced and no one capped
// is refused BEFORE anything runs: no evaluation, no reserve, no dispatch commitment, no
// settlement record, and neither the tool nor a provider behind it is called. The refusal
// is the pre-hook short circuit itself — the pipeline's only path to a tool execution — so
// "tool execution count 0" is structural, not a side effect of test wiring.
//
// THE DEFAULT IS DISABLED (Q20): with no `toolAdmission` block in the extension's config,
// every tool-call kind answers tool_not_admitted. A kind is enabled only by explicit
// configuration naming its billed dimensions with prices the deployment can stand behind —
// and the enablement is AUDITED: the plugin validates the block at construction, keeps the
// kinds that prove their pricing, refuses the misconfigured ones with the reason logged
// where an operator reads it, and exposes the admitted state for inspection. There is no
// "enabled but unpriced, bill it later" branch: an enabled kind whose dimensions carry no
// price answers tool_price_unknown, never a zero-cost guess.
//
// WHAT "CAPPABLE" MEANS HERE (v1): every billed dimension of an enabled kind must be a
// FLAT PER-REQUEST price. A flat price is enforceable by construction — the worst case of
// one call is the price itself, and the enclosing request's reservation carries it — so
// admission can vouch for the cap. A dimension priced per token (or any variable unit the
// submitter does not bound before the call) answers tool_cap_not_enforceable: the cost of
// such a call is whatever the tool does, which is the one thing admission cannot promise.
// When 20.1's dimension mapping lands, the config's declared prices are checked against the
// price snapshot instead of being the only source — this gate keeps its shape and its
// refusal codes either way.
//
// OUT OF SCOPE (v1): MCP connections (PreMCPConnectionHook) open a transport but make no
// billed call; their governance is the access seam's own. Codemode meta-tools are the
// plugin's own surface (the existing skip in PreMCPHook) and cost nothing to execute.

import (
	"github.com/maximhq/bifrost/core/schemas"
)

// The tool-call kinds the gate knows. Closed on purpose: a config naming a kind outside
// this set is a misconfiguration, refused at validation with the reason logged.
const (
	// ToolAdmissionKindMCPTool is one MCP tool execution (PreMCPHook's subject).
	ToolAdmissionKindMCPTool = "mcp_tool"
	// ToolAdmissionKindA2AAgent is one A2A agent task (PreA2AHook's subject).
	ToolAdmissionKindA2AAgent = "a2a_agent"
)

// toolAdmissionKinds is the closed set, in stable order for the audit line.
var toolAdmissionKinds = []string{ToolAdmissionKindMCPTool, ToolAdmissionKindA2AAgent}

// The gate's refusal codes. Closed, machine-readable, each one sentence of meaning:
// tool_not_admitted       未启用 — the kind is not in the validated enablement set
// tool_price_unknown      价格未知 — enabled, but a billed dimension carries no price
// tool_cap_not_enforceable 上限不可强制 — a billed dimension is priced in a unit the
//
//	submitter cannot bound before the call (v1: anything per-token)
const (
	ToolAdmissionRefusalNotAdmitted       = "tool_not_admitted"
	ToolAdmissionRefusalPriceUnknown      = "tool_price_unknown"
	ToolAdmissionRefusalCapNotEnforceable = "tool_cap_not_enforceable"
)

// ToolBilledDimension is one dimension an enabled kind bills on, with the price shape the
// deployment vouches for. Exactly one price field is set on a valid dimension: flat is the
// cappable shape, perToken is the one v1 refuses.
type ToolBilledDimension struct {
	Name string `json:"name"`
	// FlatPerRequestUsd prices one call of the kind at a fixed amount.
	FlatPerRequestUsd *float64 `json:"flatPerRequestUsd,omitempty"`
	// PerTokenUsd prices a variable unit — recorded so the refusal can name the shape.
	PerTokenUsd *float64 `json:"perTokenUsd,omitempty"`
}

// ToolAdmittedKindConfig is the operator's explicit enablement of one kind.
type ToolAdmittedKindConfig struct {
	Kind string `json:"kind"`
	// BilledDimensions names EVERY dimension the kind bills on, each with its price.
	// Empty is invalid: an enablement that proves no pricing proves nothing.
	BilledDimensions []ToolBilledDimension `json:"billedDimensions"`
}

// ToolAdmissionConfig is the `toolAdmission` block of the credit-admission extension's
// config. Nil or empty = every kind disabled (the default).
type ToolAdmissionConfig struct {
	Admitted []ToolAdmittedKindConfig `json:"admitted"`
}

// toolAdmissionState is the validated enablement the gate reads after construction: the
// kinds that proved their pricing, keyed by kind id, plus the misconfigurations the
// validation refused (kept for the audit surface, never enablement).
type toolAdmissionState struct {
	// admitted[kind] is the validated dimension list; present = enabled.
	admitted map[string][]ToolBilledDimension
	// refused records why a configured kind did not make it in, config order preserved.
	refused []toolAdmissionRefusedKind
}

type toolAdmissionRefusedKind struct {
	Kind   string
	Reason string
}

// validateToolAdmissionConfig validates the operator's block and builds the state the gate
// reads. Every refusal is a dropped enablement with its reason — the deployment never runs
// a kind whose pricing it could not read, and the reason rides on the state for the audit
// surface (and the construction log line).
func validateToolAdmissionConfig(config *ToolAdmissionConfig) *toolAdmissionState {
	state := &toolAdmissionState{admitted: map[string][]ToolBilledDimension{}}
	if config == nil {
		return state
	}
	known := map[string]bool{}
	for _, kind := range toolAdmissionKinds {
		known[kind] = true
	}
	seen := map[string]bool{}
	for _, entry := range config.Admitted {
		if !known[entry.Kind] {
			state.refused = append(state.refused, toolAdmissionRefusedKind{
				Kind:   entry.Kind,
				Reason: "unknown kind: the closed set is " + ToolAdmissionKindMCPTool + "|" + ToolAdmissionKindA2AAgent,
			})
			continue
		}
		if seen[entry.Kind] {
			state.refused = append(state.refused, toolAdmissionRefusedKind{
				Kind:   entry.Kind,
				Reason: "duplicate entry: one enablement per kind",
			})
			continue
		}
		seen[entry.Kind] = true
		if len(entry.BilledDimensions) == 0 {
			state.refused = append(state.refused, toolAdmissionRefusedKind{
				Kind:   entry.Kind,
				Reason: "no billed dimensions: an enablement that proves no pricing proves nothing",
			})
			continue
		}
		valid := true
		for _, dimension := range entry.BilledDimensions {
			if dimension.Name == "" {
				state.refused = append(state.refused, toolAdmissionRefusedKind{
					Kind:   entry.Kind,
					Reason: "a billed dimension carries no name",
				})
				valid = false
				break
			}
			flatSet := dimension.FlatPerRequestUsd != nil
			tokenSet := dimension.PerTokenUsd != nil
			if !flatSet && !tokenSet {
				state.refused = append(state.refused, toolAdmissionRefusedKind{
					Kind:   entry.Kind,
					Reason: "dimension " + dimension.Name + " carries no price; unknown is never zero cost",
				})
				valid = false
				break
			}
			if flatSet && tokenSet {
				state.refused = append(state.refused, toolAdmissionRefusedKind{
					Kind:   entry.Kind,
					Reason: "dimension " + dimension.Name + " carries two price shapes; exactly one is a price",
				})
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		state.admitted[entry.Kind] = entry.BilledDimensions
	}
	return state
}

// logToolAdmissionAudit writes the enablement's audit line at construction: what was
// admitted, what a configured-but-misread enablement cost, in config order. This is the
// record an operator diffs against the config they meant to write.
func logToolAdmissionAudit(logger schemas.Logger, state *toolAdmissionState) {
	if logger == nil {
		return
	}
	if len(state.admitted) == 0 && len(state.refused) == 0 {
		logger.Info("[tool-admission] default posture: no tool-call kind is enabled (Q20 v1); every tool call will be refused with tool_not_admitted")
		return
	}
	for _, kind := range toolAdmissionKinds {
		if dims, ok := state.admitted[kind]; ok {
			names := make([]string, 0, len(dims))
			for _, dimension := range dims {
				names = append(names, dimension.Name)
			}
			logger.Info("[tool-admission] enabled kind %s billed on %v (explicit config; audited at construction)", kind, names)
		}
	}
	for _, refused := range state.refused {
		logger.Warn("[tool-admission] refused misconfigured enablement for kind %s: %s (the kind stays disabled)", refused.Kind, refused.Reason)
	}
}

// toolAdmissionVerdict is the gate's answer for one call of one kind.
type toolAdmissionVerdict struct {
	// Admitted is true when the call may proceed past this gate.
	Admitted bool
	// RefusalCode is the closed-vocabulary code a refusal answers with ("" when admitted).
	RefusalCode string
}

// verdictFor answers the admission question for one call of one kind against the validated
// state. The order of the questions is the contract: enabled first (the default is
// disabled), then priced (unknown is never zero), then cappable (flat is the only shape v1
// can promise). A kind that passed validation cannot fail the last two — the checks below
// are the runtime twin of the validation, kept so the gate's answer never depends on the
// config having been validated at all.
func (state *toolAdmissionState) verdictFor(kind string) toolAdmissionVerdict {
	dims, enabled := state.admitted[kind]
	if !enabled {
		return toolAdmissionVerdict{Admitted: false, RefusalCode: ToolAdmissionRefusalNotAdmitted}
	}
	for _, dimension := range dims {
		if dimension.FlatPerRequestUsd == nil && dimension.PerTokenUsd == nil {
			return toolAdmissionVerdict{Admitted: false, RefusalCode: ToolAdmissionRefusalPriceUnknown}
		}
		if dimension.PerTokenUsd != nil {
			return toolAdmissionVerdict{Admitted: false, RefusalCode: ToolAdmissionRefusalCapNotEnforceable}
		}
	}
	return toolAdmissionVerdict{Admitted: true}
}

// toolAdmission is the plugin's read of its gate: inert (nil) when the credit-admission
// extension is not configured — a gateway outside the strict-billing regime keeps its
// existing tool behavior; the state, ready to answer, when it is.
type toolAdmission struct {
	state *toolAdmissionState
}

// refusal builds the short-circuit error a refused tool call is answered with: the closed
// code in both the type and the machine-readable field, one fixed sentence that names the
// kind and never the tool's arguments, the caller, or anything behind the extension.
func (gate *toolAdmission) refusal(kind string) *schemas.BifrostError {
	verdict := gate.state.verdictFor(kind)
	code := verdict.RefusalCode
	message := map[string]string{
		ToolAdmissionRefusalNotAdmitted:       "Tool calls of kind '" + kind + "' are not enabled on this gateway.",
		ToolAdmissionRefusalPriceUnknown:      "Tool calls of kind '" + kind + "' are enabled but not fully priced, so they are not admitted.",
		ToolAdmissionRefusalCapNotEnforceable: "Tool calls of kind '" + kind + "' cannot have their cost capped, so they are not admitted.",
	}[code]
	return &schemas.BifrostError{
		Type:       &code,
		StatusCode: intPtr403(),
		Error: &schemas.ErrorField{
			Message: message,
			Code:    &code,
		},
	}
}

func intPtr403() *int {
	status := 403
	return &status
}

// mcpToolAdmissionShortCircuit is the refusal as PreMCPHook returns it, with the refused
// mark set so the post hooks' accounting writes nothing for a call that never ran.
func (p *GovernancePlugin) mcpToolAdmissionShortCircuit(ctx *schemas.BifrostContext, kind string) *schemas.MCPPluginShortCircuit {
	if p.toolAdmissionGate == nil {
		return nil
	}
	verdict := p.toolAdmissionGate.state.verdictFor(kind)
	if verdict.Admitted {
		return nil
	}
	if ctx != nil {
		ctx.SetValue(governanceRejectedContextKey, true)
	}
	return &schemas.MCPPluginShortCircuit{Error: p.toolAdmissionGate.refusal(kind)}
}

// a2aToolAdmissionShortCircuit is the same refusal as PreA2AHook returns it.
func (p *GovernancePlugin) a2aToolAdmissionShortCircuit(ctx *schemas.BifrostContext, kind string) *schemas.A2APluginShortCircuit {
	if p.toolAdmissionGate == nil {
		return nil
	}
	verdict := p.toolAdmissionGate.state.verdictFor(kind)
	if verdict.Admitted {
		return nil
	}
	if ctx != nil {
		ctx.SetValue(governanceRejectedContextKey, true)
	}
	return &schemas.A2APluginShortCircuit{Error: p.toolAdmissionGate.refusal(kind)}
}

// ToolAdmissionState answers what the gate would do with each kind, for the admin surface
// and the tests: one entry per closed-set kind, admitted or the refusal it would answer
// with. The answer is derived from the validated state, never from the raw config.
func (p *GovernancePlugin) ToolAdmissionState() map[string]string {
	answer := map[string]string{}
	for _, kind := range toolAdmissionKinds {
		if p.toolAdmissionGate == nil {
			answer[kind] = "unmanaged"
			continue
		}
		verdict := p.toolAdmissionGate.state.verdictFor(kind)
		if verdict.Admitted {
			answer[kind] = "admitted"
		} else {
			answer[kind] = verdict.RefusalCode
		}
	}
	return answer
}

// toolExecutionAdmitted answers whether THIS request type is a tool execution whose kind
// the tool admission gate has enabled — the structural exception the credit-admission gate
// grants it (see evaluateCreditAdmission). Fail-closed by construction: an extension without
// the gate, a gate without the kind, or a request type that is not a tool execution all
// answer false, and the model-side gates keep their own answers.
func (p *GovernancePlugin) toolExecutionAdmitted(requestType schemas.RequestType) bool {
	if requestType != schemas.MCPToolExecutionRequest {
		return false
	}
	if p.toolAdmissionGate == nil {
		return false
	}
	return p.toolAdmissionGate.state.verdictFor(ToolAdmissionKindMCPTool).Admitted
}
