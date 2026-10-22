package governance

// Tool admission (20.2): the gate's contract, held to the same standard the credit refusals
// are — a refusal is the pre-hook SHORT CIRCUIT itself (the pipeline's only path to a tool
// execution, so "tool execution count 0" is structural), it carries a machine-readable code
// from the closed three-code vocabulary, it leaves no dispatch commitment, no settlement
// record and no reserve call behind, and the default posture is DISABLED until the operator
// explicitly enables a kind whose pricing the config can state completely.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/grant"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flatToolAdmission is an admission config whose toolAdmission block enables one kind with
// one flat per-request priced dimension — the cappable shape.
func flatToolAdmission() *CreditAdmissionConfig {
	flat := 0.001
	admission := admissionWithReserve("http://unused.invalid", 0)
	admission.ToolAdmission = &ToolAdmissionConfig{
		Admitted: []ToolAdmittedKindConfig{
			{
				Kind: ToolAdmissionKindMCPTool,
				BilledDimensions: []ToolBilledDimension{
					{Name: "tool_call", FlatPerRequestUsd: &flat},
				},
			},
		},
	}
	return admission
}

// toolRefusalCodeOf runs one MCP tool call through the hook and answers the refusal's
// machine-readable code, failing when the call was NOT refused — the inverse direction has
// its own assertions in the admitted cases.
func toolRefusalCodeOf(t *testing.T, plugin *GovernancePlugin, ctx *schemas.BifrostContext) string {
	t.Helper()
	_, shortCircuit, err := plugin.PreMCPHook(ctx, mcpToolCall("sentry-read_file"))
	require.NoError(t, err, "a tool-admission refusal is a short circuit, never a plugin error")
	require.NotNil(t, shortCircuit, "the tool call reached execution instead of being turned away")
	require.NotNil(t, shortCircuit.Error)
	require.NotNil(t, shortCircuit.Error.Error)
	require.NotNil(t, shortCircuit.Error.Error.Code, "the refusal carries a machine-readable code")
	require.NotNil(t, shortCircuit.Error.StatusCode)
	assert.Equal(t, 403, *shortCircuit.Error.StatusCode)
	return *shortCircuit.Error.Error.Code
}

// --- the default is disabled ---------------------------------------------------------------

func TestToolAdmissionDefaultIsDisabledAndLeavesNothingBehind(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	admission := admissionWithReserve(fake.endpoint(), 0)
	plugin := newReserveTestPlugin(t, admission) // extension on, NO toolAdmission block

	ctx := reserveTestCtx()
	code := toolRefusalCodeOf(t, plugin, ctx)
	assert.Equal(t, ToolAdmissionRefusalNotAdmitted, code)
	_, shortCircuit, err := plugin.PreMCPHook(ctx, mcpToolCall("sentry-read_file"))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey).(bool),
		"a refused tool call is marked refused so post-hook accounting writes nothing")

	// Nothing was committed, settled or reserved: the gate refused before everything.
	assert.Equal(t, 0, fake.calls(), "no reserve call for a refused tool call")
	pending, err := plugin.dispatchCommitments.ListPending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending, "no dispatch commitment for a refused tool call")
	assert.Empty(t, plugin.settlementOutbox.(*localSettlementOutbox).records,
		"no settlement record for a refused tool call")

	state := plugin.ToolAdmissionState()
	assert.Equal(t, ToolAdmissionRefusalNotAdmitted, state[ToolAdmissionKindMCPTool])
	assert.Equal(t, ToolAdmissionRefusalNotAdmitted, state[ToolAdmissionKindA2AAgent])
}

// --- explicit enablement with complete flat pricing ----------------------------------------

func TestToolAdmissionAdmitsAKindPricedFlatPerRequest(t *testing.T) {
	plugin := newReserveTestPlugin(t, flatToolAdmission())
	state := plugin.ToolAdmissionState()
	assert.Equal(t, "admitted", state[ToolAdmissionKindMCPTool])

	// An unrestricted caller (no credential → no access narrowing) on an admitted kind:
	// the tool gate is the only thing that could refuse this call, and it does not.
	ctx := anonymousToolCtx()
	_, shortCircuit, err := plugin.PreMCPHook(ctx, mcpToolCall("sentry-read_file"))
	require.NoError(t, err)
	assert.Nil(t, shortCircuit, "an admitted kind's tool call proceeds past the gate")
}

// --- misconfigured enablement is refused, with the reason audited --------------------------

func TestToolAdmissionRefusesEnablementThatCannotStateItsPricing(t *testing.T) {
	admission := admissionWithReserve("http://unused.invalid", 0)
	admission.ToolAdmission = &ToolAdmissionConfig{
		Admitted: []ToolAdmittedKindConfig{
			{Kind: "teleport"},                 // outside the closed kind set
			{Kind: ToolAdmissionKindMCPTool},   // no billed dimensions
			{Kind: ToolAdmissionKindA2AAgent, BilledDimensions: []ToolBilledDimension{
				{Name: "a2a_call"}, // a dimension with no price
			}},
		},
	}
	plugin := newReserveTestPlugin(t, admission)

	state := plugin.ToolAdmissionState()
	assert.Equal(t, ToolAdmissionRefusalNotAdmitted, state[ToolAdmissionKindMCPTool],
		"an enablement with no dimensions proves no pricing and stays disabled")
	assert.Equal(t, ToolAdmissionRefusalNotAdmitted, state[ToolAdmissionKindA2AAgent],
		"an enablement with an unpriced dimension stays disabled")

	code := toolRefusalCodeOf(t, plugin, reserveTestCtx())
	assert.Equal(t, ToolAdmissionRefusalNotAdmitted, code)
}

// --- the three verdicts of the runtime twin ------------------------------------------------

func TestToolAdmissionVerdictCodes(t *testing.T) {
	flat := 0.001
	perToken := 0.000002

	state := &toolAdmissionState{admitted: map[string][]ToolBilledDimension{}}
	assert.Equal(t, ToolAdmissionRefusalNotAdmitted, state.verdictFor(ToolAdmissionKindMCPTool).RefusalCode,
		"a kind outside the enablement set answers not-admitted")

	state.admitted[ToolAdmissionKindMCPTool] = []ToolBilledDimension{{Name: "tool_call"}}
	assert.Equal(t, ToolAdmissionRefusalPriceUnknown, state.verdictFor(ToolAdmissionKindMCPTool).RefusalCode,
		"a dimension with no price answers price-unknown, never zero cost")

	state.admitted[ToolAdmissionKindMCPTool] = []ToolBilledDimension{
		{Name: "tool_call", FlatPerRequestUsd: &flat},
		{Name: "tool_tokens", PerTokenUsd: &perToken},
	}
	assert.Equal(t, ToolAdmissionRefusalCapNotEnforceable, state.verdictFor(ToolAdmissionKindMCPTool).RefusalCode,
		"a per-token dimension is a cost the submitter cannot bound before the call")

	state.admitted[ToolAdmissionKindMCPTool] = []ToolBilledDimension{
		{Name: "tool_call", FlatPerRequestUsd: &flat},
	}
	assert.True(t, state.verdictFor(ToolAdmissionKindMCPTool).Admitted,
		"all dimensions flat per request is the cappable shape")
}

// --- the gate is inert without the extension ----------------------------------------------

func TestToolAdmissionIsInertWithoutTheCreditAdmissionExtension(t *testing.T) {
	plugin := newReserveTestPlugin(t, nil) // extension absent: upstream behavior

	// The hook runs its own flow unchanged: whatever IT answers, the answer is not the
	// tool gate's — no tool-admission code exists to answer with (the access seam's own
	// refusal for an unlisted tool is the upstream behavior this test must not perturb).
	_, shortCircuit, err := plugin.PreMCPHook(reserveTestCtx(), mcpToolCall("sentry-read_file"))
	require.NoError(t, err)
	if shortCircuit != nil && shortCircuit.Error != nil && shortCircuit.Error.Error != nil && shortCircuit.Error.Error.Code != nil {
		assert.NotEqual(t, ToolAdmissionRefusalNotAdmitted, *shortCircuit.Error.Error.Code,
			"a gateway outside the strict-billing regime keeps its tool behavior")
	}

	assert.Equal(t, "unmanaged", plugin.ToolAdmissionState()[ToolAdmissionKindMCPTool])
}

// --- A2A rides the same gate, before the agent gates ---------------------------------------

func TestToolAdmissionGatesA2ABeforeTheAgentGates(t *testing.T) {
	// Default posture: the A2A kind is not enabled, so the call dies at the tool gate
	// before the agent-enablement check could answer its own (different) refusal.
	plugin := newReserveTestPlugin(t, flatToolAdmission()) // only the MCP kind enabled
	ctx := a2aVKCtx(creditRefusalTestVKValue)

	_, shortCircuit, err := plugin.PreA2AHook(ctx, operationReq("some-agent"))
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "an unenabled A2A kind is refused")
	require.NotNil(t, shortCircuit.Error)
	require.NotNil(t, shortCircuit.Error.Error.Code)
	assert.Equal(t, ToolAdmissionRefusalNotAdmitted, *shortCircuit.Error.Error.Code,
		"the refusal is the tool gate's, not the agent gate's — the tool gate ran first")
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey).(bool))

	// The state surface agrees with what the hook answered.
	assert.Equal(t, ToolAdmissionRefusalNotAdmitted, plugin.ToolAdmissionState()[ToolAdmissionKindA2AAgent])
}

// --- the enablement is audited at construction ---------------------------------------------

func TestToolAdmissionAuditsTheEnablementAtConstruction(t *testing.T) {
	admission := flatToolAdmission()
	plugin := newReserveTestPlugin(t, admission)
	if mock, ok := plugin.logger.(*MockLogger); ok {
		found := false
		for _, line := range mock.infos {
			if strings.Contains(line, "[tool-admission]") && strings.Contains(line, "enabled kind") {
				found = true
			}
		}
		assert.True(t, found, "the construction audit names the enabled kind: %v", mock.infos)
	}
}


// anonymousToolCtx carries no credential at all: no identity presented, no access to
// narrow anything — the caller the existing seams treat as unrestricted.
func anonymousToolCtx() *schemas.BifrostContext {
	ctx := schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
	ctx.SetGrant(grant.New())
	return ctx
}
