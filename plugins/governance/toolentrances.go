package governance

/*
 * The tool-execution entrance registry (OpenBot Bifrost 20.1/20.4, #341/#344): the closed
 * enumeration of every call type that can execute a tool or attach an extra call, with the
 * surcharge dimension each bills on — the Go half of the contract whose TS half lives in
 * OpenBot at server/src/billing/tool-surcharge-pricing.ts (that registry is the contract's
 * source of truth; this mirror exists so the gateway side can answer 22.1's all-entrances
 * audit without crossing the language seam, and the test below pins the two halves agree).
 *
 * COST ON THE REQUEST FLOW: none. The registry is a package-level slice built at init from
 * constants; nothing on the request path reads it (the hooks enforce through their own
 * existing checks, listed per entrance below). It exists for audits and for the admission
 * wiring 20.2 (#342) consumes on the OpenBot side.
 *
 * Each entrance names the hook that can execute it on this plugin, so "no entrance executes
 * ungoverned" is checkable: the refusal each hook already owns is what keeps its entrance
 * closed to a caller that has not been admitted (allow-list, transport approval, injected
 * marker or codemode stamp), and ToolBypass* tests in toolentrances_test.go pin those
 * refusals entrance by entrance.
 */

// ToolEntrance is one entrance in the tool/route call registry.
type ToolEntrance struct {
	// ID is the entrance's stable id, identical to the TS registry's id (the closed set
	// tool_call_charges.entry accepts on the OpenBot side).
	ID string
	// Surface describes the entrance in one line.
	Surface string
	// Anchor names the hook or module that executes this entrance.
	Anchor string
	// Dimension is the surcharge dimension a call through this entrance bills on; ""
	// for the enumerated non-entrances.
	Dimension string
	// BillsIdentity is true when a call through this entrance mints an OpenBot
	// tool-call charge identity row.
	BillsIdentity bool
	// NotAnEntranceReason carries, exactly for the non-entrances, why they are none.
	NotAnEntranceReason string
}

// ToolEntrances is the complete registry: five identity entrances, the route-attached
// model call, and the two enumerated non-entrances. The order matches the TS registry.
var ToolEntrances = []ToolEntrance{
	{
		ID:            "mcp.tool_call",
		Surface:       "A vendor MCP tool execution through the MCP transport.",
		Anchor:        "GovernancePlugin.PreMCPHook / PostMCPHook",
		Dimension:     "surcharge_tool_call",
		BillsIdentity: true,
	},
	{
		ID:            "injected.tool_call",
		Surface:       "A provider-injected tool Bifrost itself executes when the model calls it.",
		Anchor:        "core/injectedauth.go InjectedToolAuthorized (read at PreMCPHook)",
		Dimension:     "surcharge_tool_call",
		BillsIdentity: true,
	},
	{
		ID:            "injected.web_search",
		Surface:       "The server-side web-search tool family the wire prices per request.",
		Anchor:        "core/injectedtools.go (native web search tool)",
		Dimension:     "surcharge_web_search",
		BillsIdentity: true,
	},
	{
		ID:            "responses.tool_call",
		Surface:       "A tool a Responses request declares in its own input, hoisted before wire conversion.",
		Anchor:        "core/responsestools.go hoistResponsesAdditionalTools",
		Dimension:     "surcharge_tool_call",
		BillsIdentity: true,
	},
	{
		ID:            "a2a.call",
		Surface:       "An agent-to-agent call through the A2A transport.",
		Anchor:        "GovernancePlugin.PreA2AHook / PostA2AHook",
		Dimension:     "surcharge_a2a_call",
		BillsIdentity: true,
	},
	{
		ID:      "routing.embed",
		Surface: "The routing layer's attached embedding call (warmup and hot-path classification).",
		Anchor:  "GovernancePlugin.AttributeRoutingEmbeddingCost (routingembedcost.go)",
		NotAnEntranceReason: "a model call the router makes: it bills on the attached model's own token " +
			"dimensions through the request's reservation, never a flat surcharge",
	},
	{
		ID:      "mcp.connection",
		Surface: "An MCP connection setup.",
		Anchor:  "GovernancePlugin.PreMCPConnectionHook / PostMCPConnectionHook",
		NotAnEntranceReason: "connection plumbing, not a per-call fee: no tool executes, nothing is " +
			"billed per connection",
	},
	{
		ID:      "codemode.meta_tool",
		Surface: "A codemode meta-tool (listToolFiles, readToolFile, getToolDocs).",
		Anchor:  "GovernancePlugin.PreMCPHook (bifrost.IsCodemodeTool branch)",
		NotAnEntranceReason: "codemode meta-tools are discovery, not vendor tool executions — the hook " +
			"stamps their include-list and skips evaluation",
	},
}

// ToolEntranceByID answers the registry entry for one id, or nil when the id is outside
// the closed set — the lookup the all-entrances audit and the admission wiring read.
func ToolEntranceByID(id string) *ToolEntrance {
	for i := range ToolEntrances {
		if ToolEntrances[i].ID == id {
			return &ToolEntrances[i]
		}
	}
	return nil
}
