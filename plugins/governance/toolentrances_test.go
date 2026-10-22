package governance

/*
 * OpenBot Bifrost 20.4 (#344), the gateway half: no tool execution entrance executes
 * ungoverned. Every entrance in ToolEntrances has a refusal this plugin already owns, and
 * the ToolBypass tests below pin those refusals entrance by entrance, at the hook a
 * deployment actually calls (not the verdict behind it) — the same discipline
 * mcpauthorization_test.go keeps for the approval binding itself.
 *
 * The billing-side refusals (unpriced surcharge dimensions refusing before dispatch, the
 * personal-key/company-tool isolation, the audit trail) are OpenBot-side behavior pinned
 * in server/tests/tool-isolation.test.ts; what this file pins is the gateway's own share:
 * the hook-level refusals that make "bypass" impossible before the request ever reaches a
 * provider or a tool.
 */

import (
	"reflect"
	"strings"
	"testing"

	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mcpToolCallRequest builds the tool-execution request shape PreMCPHook gates on.
func toolBypassMCPRequest(client, tool string) *schemas.BifrostMCPRequest {
	return &schemas.BifrostMCPRequest{
		RequestType: schemas.MCPRequestTypeChatToolCall,
		ClientName:  client,
		ChatAssistantMessageToolCall: &schemas.ChatAssistantMessageToolCall{
			Function: schemas.ChatAssistantMessageToolCallFunction{
				Name:      &tool,
				Arguments: "{}",
			},
		},
	}
}

// ToolBypassMCPEntranceRefusesAToolTheGrantDoesNotPermit: the MCP entrance is closed to a
// caller whose access does not list the tool and that carries no other approval — the hook
// short-circuits, marks the request refused, and nothing downstream (tool, provider) runs.
func TestToolBypassMCPEntranceRefusesAToolTheGrantDoesNotPermit(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping(nil), false)
	ctx := presentCtx(mcpTestVKValue)

	_, short, err := p.PreMCPHook(ctx, toolBypassMCPRequest("local", "local-read"))
	require.NoError(t, err)
	require.NotNil(t, short, "an unpermitted tool must be turned away at the entrance")
	require.NotNil(t, short.Error)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
		"a refused tool call is marked refused for everything downstream")
	require.NotNil(t, short.Error.StatusCode, "the refusal carries a status a client can act on")
}

// ToolBypassApprovalDoesNotFollowARetargetedRequest: a transport approval binds one exact
// client+tool pair; the same key asking for a sibling tool or another client's tool is a
// fresh unpermitted execution and is refused — an approval can never be replayed onto a
// different target.
func TestToolBypassApprovalDoesNotFollowARetargetedRequest(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping(nil), false)

	for _, tc := range []struct{ name, client, tool string }{
		{"a sibling tool of the approved one", "local", "local-write"},
		{"another client's tool of the approved name", "other", "local-read"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := presentCtx(mcpTestVKValue)
			SetMCPExecutionAuthorization(ctx, "local", "local-read")
			_, short, err := p.PreMCPHook(ctx, toolBypassMCPRequest(tc.client, tc.tool))
			require.NoError(t, err)
			require.NotNil(t, short, "a retargeted approval is no approval")
		})
	}

	// The exact approved pair still passes — the binding narrows, never widens.
	ctx := presentCtx(mcpTestVKValue)
	SetMCPExecutionAuthorization(ctx, "local", "local-read")
	_, short, err := p.PreMCPHook(ctx, toolBypassMCPRequest("local", "local-read"))
	require.NoError(t, err)
	assert.Nil(t, short)
}

// ToolBypassInjectedToolMarkerBindsOneExactExecution: the injected-tool marker is the
// second approval source, and it too names one exact client+tool pair — the operator chose
// that tool in the provider config, and the choice does not extend to its siblings.
func TestToolBypassInjectedToolMarkerBindsOneExactExecution(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping(nil), false)
	restore := isInjectedToolExecution
	t.Cleanup(func() { isInjectedToolExecution = restore })
	isInjectedToolExecution = func(_ *schemas.BifrostContext, clientName, toolName string) bool {
		return clientName == "local" && toolName == "local-write"
	}

	_, short, err := p.PreMCPHook(presentCtx(mcpTestVKValue), toolBypassMCPRequest("local", "local-delete"))
	require.NoError(t, err)
	require.NotNil(t, short, "a marker for one tool is not a marker for its sibling")

	_, short, err = p.PreMCPHook(presentCtx(mcpTestVKValue), toolBypassMCPRequest("local", "local-write"))
	require.NoError(t, err)
	assert.Nil(t, short, "the exact marked tool passes — the operator chose it in the provider config")
}

// ToolBypassCodemodeMetaToolsAreDiscoveryNotExecution: a codemode meta-tool is stamped and
// passed for its own discovery semantics — the entrance registry enumerates it precisely so
// 22.1 finds it accounted for as a NON-entrance rather than as an ungoverned execution.
func TestToolBypassCodemodeMetaToolsAreDiscoveryNotExecution(t *testing.T) {
	p := newPluginForMCPStamping(t, buildVKForMCPStamping([]string{"read_file"}), false)
	ctx := presentCtx(mcpTestVKValue)

	name := "listToolFiles"
	require.True(t, bifrost.IsCodemodeTool(name), "the fixture must name a real codemode meta-tool")
	_, short, err := p.PreMCPHook(ctx, toolBypassMCPRequest("codemode", name))
	require.NoError(t, err)
	assert.Nil(t, short, "a codemode meta-tool is not governed as a vendor tool execution")
	// The stamp is the meta-tool's own governance: its include-list is narrowed to the
	// grant, so its DISCOVERY cannot enumerate tools the caller was not granted.
	assert.NotNil(t, ctx.Value(schemas.MCPContextKeyIncludeTools),
		"the codemode branch stamps the include-list before discovery")
}

// ToolBypassEntranceRegistryMatchesTheHooksThatExist: every anchor the registry names
// resolves to a real method on the plugin (or names a core module), every identity
// entrance bills a surcharge dimension from the closed set, and every non-entrance says
// why it is none — a hook that moves without its entrance breaks this, not 22.1.
func TestToolBypassEntranceRegistryMatchesTheHooksThatExist(t *testing.T) {
	pluginType := reflect.TypeOf(&GovernancePlugin{})
	surchargeDimensions := map[string]bool{
		"surcharge_tool_call":  true,
		"surcharge_web_search": true,
		"surcharge_a2a_call":   true,
	}

	identityEntrances := 0
	for _, entrance := range ToolEntrances {
		if entrance.ID == "" || entrance.Surface == "" || entrance.Anchor == "" {
			t.Fatalf("entrance %+v carries an empty identity field", entrance)
		}
		if strings.HasPrefix(entrance.Anchor, "GovernancePlugin.") {
			method := strings.TrimPrefix(entrance.Anchor, "GovernancePlugin.")
			method = strings.SplitN(method, " ", 2)[0]
			_, ok := pluginType.MethodByName(method)
			require.True(t, ok, "%s: anchor names %s, which is not a method on GovernancePlugin", entrance.ID, method)
		}
		if entrance.BillsIdentity {
			identityEntrances++
			require.True(t, surchargeDimensions[entrance.Dimension],
				"%s: identity entrances bill a surcharge dimension, got %q", entrance.ID, entrance.Dimension)
			assert.Empty(t, entrance.NotAnEntranceReason, "%s: an identity entrance is not a non-entrance", entrance.ID)
		} else {
			require.Empty(t, entrance.Dimension, "%s: a non-entrance bills no dimension", entrance.ID)
			require.NotEmpty(t, entrance.NotAnEntranceReason, "%s: a non-entrance must say why it is none", entrance.ID)
		}
	}
	// The five identity entrances — the closed set the OpenBot table CHECK accepts.
	assert.Equal(t, 5, identityEntrances, "the registry must carry exactly the five identity entrances")
}

// ToolBypassEntranceByIdRefusesUnknownIds: the lookup answers nil for anything outside
// the closed set — an id the registry never listed can never be admitted by name.
func TestToolBypassEntranceByIdRefusesUnknownIds(t *testing.T) {
	require.NotNil(t, ToolEntranceByID("mcp.tool_call"))
	require.NotNil(t, ToolEntranceByID("codemode.meta_tool"))
	assert.Nil(t, ToolEntranceByID("made.up.entrance"))
}
