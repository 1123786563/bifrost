package governance

// The OpenBot credit-admission extension is the mount point the OpenBot integration is built on:
// a gateway that cannot prove the extension is enabled must not send provably-paid work to a
// provider, on any inference path, for any credential state, on any attempt of a fallback chain.
// These tests assert that hard rule through the hooks a deployment actually calls, the way
// hookrefusal_test.go does, because only that path carries what the caller is answered with and
// the refusal mark that keeps an undispatched request from being billed.
//
// The gate refuses what is KNOWN paid: a pricing entry with a strictly positive rate. Explicitly
// free models (an entry whose every rate is absent or zero) stay servable, and models the catalog
// says nothing about are left to the price-lock gate that owns unknown pricing - this gate answers
// "may paid work be dispatched at all", not "is this model priced".

import (
	"context"
	"encoding/json"
	"regexp"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// admissionTestVKValue is the key value the admission tests' store serves.
const admissionTestVKValue = "sk-bf-admission-vk"

// admissionPaidModel is a model the test catalog prices: serving it spends money.
const admissionPaidModel = "gpt-4o-admission-paid"

// admissionFreeModel is a model the test catalog prices at exactly zero everywhere.
const admissionFreeModel = "gpt-4o-admission-free"

// admissionTestPricingRows builds the catalog the admission tests run against: one paid model,
// one explicitly free model, and nothing else.
func admissionTestPricingRows() []configstoreTables.TableModelPricing {
	zero := 0.0
	price := 0.0005
	return []configstoreTables.TableModelPricing{
		{Model: admissionPaidModel, Provider: string(schemas.OpenAI), Mode: "chat", InputCostPerToken: &price},
		{Model: admissionFreeModel, Provider: string(schemas.OpenAI), Mode: "chat", InputCostPerToken: &zero, OutputCostPerToken: &zero},
	}
}

// newAdmissionTestPlugin builds a governance plugin serving one virtual key, with the admission
// switch in the state the test asks for and a catalog holding the pricing rows above.
func newAdmissionTestPlugin(t *testing.T, admission *CreditAdmissionConfig, rows []configstoreTables.TableModelPricing) *GovernancePlugin {
	t.Helper()
	vk := buildVirtualKeyWithProviders("vk-admission", admissionTestVKValue, "admission-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai",
			[]string{admissionPaidModel, admissionFreeModel, "model-no-catalog-entry"})})
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, &mockInMemoryStore{})
	require.NoError(t, err)

	var catalog *modelcatalog.ModelCatalog
	if rows != nil {
		ds := datasheet.NewTestStore(nil)
		ds.SetPricingRowsForTest(rows)
		catalog = modelcatalog.NewTestCatalogWithDatasheet(ds)
	}

	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory:          boolPtr(false),
		OpenBotCreditAdmission: admission,
	}, logger, store, nil, catalog, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

// admissionChatRequest returns a chat request for the given model with the provider already
// resolved, the same shape newChatRequest uses.
func admissionChatRequest(model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: model},
	}
}

// admissionRefusalFor runs a request through the LLM hook and returns the error the caller is
// answered with, asserting the request was turned away and marked like every other refusal.
func admissionRefusalFor(t *testing.T, plugin *GovernancePlugin, ctx *schemas.BifrostContext, req *schemas.BifrostRequest) *schemas.BifrostError {
	t.Helper()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	require.NotNil(t, shortCircuit, "the request reached the provider instead of being turned away")
	require.NotNil(t, shortCircuit.Error, "a refused request has to carry the reason it was refused")
	require.NotNil(t, shortCircuit.Error.Error)
	assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
		"a refused request is marked refused for everything downstream")
	return shortCircuit.Error
}

// --- the extension's negotiable identity ----------------------------------------------
//
// OpenBot's capability negotiation looks the extension up by name in the gateway's plugin
// manifest and trusts exactly one major line. The declaration is a contract, so its shape is
// asserted the way the consumer reads it: the negotiation's name, a strict X.Y.Z version, and
// a major the deployment can trust.

func TestCreditAdmissionExtensionDeclaresANegotiableIdentity(t *testing.T) {
	assert.Equal(t, "openbot-credit-admission", CreditAdmissionExtensionName,
		"the negotiation looks the extension up by this exact name")

	majorPattern := regexp.MustCompile(`^(\d+)\.\d+\.\d+$`)
	match := majorPattern.FindStringSubmatch(CreditAdmissionExtensionVersion)
	require.NotNil(t, match,
		"the version is strict X.Y.Z, the only shape the negotiation echoes")
	assert.Equal(t, "1", match[1],
		"the negotiation trusts major 1 only (>=1.0.0 <2.0.0)")
}

func TestCreditAdmissionExtensionDescriptorReflectsTheSwitch(t *testing.T) {
	t.Run("no config block", func(t *testing.T) {
		plugin := newAdmissionTestPlugin(t, nil, admissionTestPricingRows())
		descriptor := plugin.CreditAdmissionExtension()
		assert.Equal(t, CreditAdmissionExtensionName, descriptor.Name)
		assert.Equal(t, CreditAdmissionExtensionVersion, descriptor.Version)
		assert.False(t, descriptor.Enabled, "an unconfigured extension is not enabled")
	})
	t.Run("switch off", func(t *testing.T) {
		plugin := newAdmissionTestPlugin(t, &CreditAdmissionConfig{Enabled: boolPtr(false)}, admissionTestPricingRows())
		assert.False(t, plugin.CreditAdmissionExtension().Enabled)
	})
	t.Run("switch on", func(t *testing.T) {
		plugin := newAdmissionTestPlugin(t, &CreditAdmissionConfig{Enabled: boolPtr(true)}, admissionTestPricingRows())
		assert.True(t, plugin.CreditAdmissionExtension().Enabled)
	})
}

func TestCreditAdmissionConfigUnmarshalsFromThePluginConfig(t *testing.T) {
	var cfg Config
	require.NoError(t, json.Unmarshal([]byte(`{"openbot_credit_admission": {"enabled": true}}`), &cfg))
	require.NotNil(t, cfg.OpenBotCreditAdmission)
	require.NotNil(t, cfg.OpenBotCreditAdmission.Enabled)
	assert.True(t, *cfg.OpenBotCreditAdmission.Enabled)

	var absent Config
	require.NoError(t, json.Unmarshal([]byte(`{}`), &absent))
	assert.Nil(t, absent.OpenBotCreditAdmission, "no block means the extension is not configured")
}

// --- the hard rule: admission not enabled refuses paid work ----------------------------

func TestPreLLMHookRefusesAPaidModelWhenAdmissionIsDisabled(t *testing.T) {
	plugin := newAdmissionTestPlugin(t, &CreditAdmissionConfig{Enabled: boolPtr(false)}, admissionTestPricingRows())
	ctx := presentCtx(admissionTestVKValue)

	bifrostErr := admissionRefusalFor(t, plugin, ctx, admissionChatRequest(admissionPaidModel))

	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 503, *bifrostErr.StatusCode,
		"the refusal is the deployment's posture, not the caller's mistake")
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditAdmissionNotEnabled), *bifrostErr.Type,
		"the refusal carries its own explainable type, not a generic error")
	assert.Contains(t, bifrostErr.Error.Message, "not enabled",
		"the refusal says admission is what is missing")
	assert.Contains(t, bifrostErr.Error.Message, CreditAdmissionExtensionName,
		"the refusal names the extension a deployment mounts and enables")
}

func TestPreLLMHookRefusesAPaidModelWhenTheExtensionIsNotConfigured(t *testing.T) {
	plugin := newAdmissionTestPlugin(t, nil, admissionTestPricingRows())
	ctx := presentCtx(admissionTestVKValue)

	bifrostErr := admissionRefusalFor(t, plugin, ctx, admissionChatRequest(admissionPaidModel))

	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditAdmissionNotEnabled), *bifrostErr.Type)
}

// A fallback chain is not a bypass: every attempt runs the hook again, so an admission refusal
// holds on the fallback too, and streaming is the same hook.
func TestPreLLMHookRefusesAPaidModelOnEveryFallbackAttemptAndOnStreams(t *testing.T) {
	plugin := newAdmissionTestPlugin(t, &CreditAdmissionConfig{Enabled: boolPtr(false)}, admissionTestPricingRows())
	ctx := presentCtx(admissionTestVKValue)

	first := admissionRefusalFor(t, plugin, ctx, admissionChatRequest(admissionPaidModel))
	require.NotNil(t, first)

	// The attempt a fallback chain would try next lands in the same funnel.
	second := admissionRefusalFor(t, plugin, ctx, admissionChatRequest(admissionPaidModel))
	require.NotNil(t, second.Type)
	assert.Equal(t, string(DecisionCreditAdmissionNotEnabled), *second.Type)

	// Streaming dispatches through the same PreLLMHook (RunStreamPreHooks).
	streamReq := &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionStreamRequest,
		ChatRequest: &schemas.BifrostChatRequest{Provider: schemas.OpenAI, Model: admissionPaidModel},
	}
	third := admissionRefusalFor(t, plugin, ctx, streamReq)
	require.NotNil(t, third.Type)
	assert.Equal(t, string(DecisionCreditAdmissionNotEnabled), *third.Type)
}

// The gate applies before any grant machinery answers, so a key-less request cannot walk past it
// on a deployment that presents no credential requirement either.
func TestAdmissionGateAppliesToARequestThatPresentsNothing(t *testing.T) {
	plugin := newAdmissionTestPlugin(t, &CreditAdmissionConfig{Enabled: boolPtr(false)}, admissionTestPricingRows())
	ctx := emptyCtx()

	bifrostErr := admissionRefusalFor(t, plugin, ctx, admissionChatRequest(admissionPaidModel))

	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditAdmissionNotEnabled), *bifrostErr.Type)
}

// Explicitly free stays free: a model priced at exactly zero everywhere costs nothing to serve,
// so a deployment with the switch off keeps serving it.
func TestPreLLMHookAllowsAnExplicitlyFreeModelWhenAdmissionIsDisabled(t *testing.T) {
	plugin := newAdmissionTestPlugin(t, &CreditAdmissionConfig{Enabled: boolPtr(false)}, admissionTestPricingRows())
	ctx := presentCtx(admissionTestVKValue)

	_, shortCircuit, err := plugin.PreLLMHook(ctx, admissionChatRequest(admissionFreeModel))

	require.NoError(t, err)
	require.Nil(t, shortCircuit, "an explicitly free model is not paid work")
	assert.Nil(t, ctx.Value(governanceRejectedContextKey))
}

// Unknown pricing is a different gate's question. This gate refuses what is known paid; a model
// the catalog says nothing about is left to the price-lock admission that runs once the
// extension is enabled (and to OpenBot's price snapshot before that).
func TestPreLLMHookLeavesUnknownPricingToThePriceLockGateWhenAdmissionIsDisabled(t *testing.T) {
	plugin := newAdmissionTestPlugin(t, &CreditAdmissionConfig{Enabled: boolPtr(false)}, admissionTestPricingRows())
	ctx := presentCtx(admissionTestVKValue)

	_, shortCircuit, err := plugin.PreLLMHook(ctx, admissionChatRequest("model-no-catalog-entry"))

	require.NoError(t, err)
	require.Nil(t, shortCircuit, "unknown pricing is not known-paid work")
}

// With the extension enabled, a paid model's fate is the reserve seam's (07.2): it proceeds
// only once a reservation is held. A deployment that enabled the extension but wired no reserve
// door cannot prove a hold, so the dispatchable work is refused rather than passed unreserved.
// The success path - a held reservation continuing the request - is the reserve seam's own
// contract, asserted against a fake billing service in creditreserve_test.go.
func TestPreLLMHookRefusesAPaidModelWhenEnabledWithoutAReserveDoor(t *testing.T) {
	plugin := newAdmissionTestPlugin(t, &CreditAdmissionConfig{Enabled: boolPtr(true)}, admissionTestPricingRows())
	ctx := presentCtx(admissionTestVKValue)

	bifrostErr := admissionRefusalFor(t, plugin, ctx, admissionChatRequest(admissionPaidModel))

	require.NotNil(t, bifrostErr.StatusCode)
	assert.Equal(t, 503, *bifrostErr.StatusCode,
		"the refusal is the deployment's posture, not the caller's mistake")
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type,
		"the extension is enabled, so this is not an admission-not-enabled refusal")
	assert.Contains(t, bifrostErr.Error.Message, "no reserve door is wired",
		"the refusal says what the deployment is missing")
}

// --- every inference funnel goes through the gate ---------------------------------------
//
// The gate lives in the one funnel every evaluating caller passes through, so the entries below
// are the same question asked of each pipeline: realtime admission (which evaluates before any
// turn exists) and the embedding path (a dispatch of its own).

func TestAdmissionGateCoversEveryInferenceFunnel(t *testing.T) {
	plugin := newAdmissionTestPlugin(t, &CreditAdmissionConfig{Enabled: boolPtr(false)}, admissionTestPricingRows())

	t.Run("realtime admission", func(t *testing.T) {
		ctx := presentCtx(admissionTestVKValue)
		_, bifrostErr := plugin.Evaluate(ctx, &EvaluationRequest{
			RequestType: schemas.RealtimeRequest,
			Provider:    schemas.OpenAI,
			Model:       admissionPaidModel,
		})
		require.NotNil(t, bifrostErr, "the realtime upgrade is refused before a session opens")
		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditAdmissionNotEnabled), *bifrostErr.Type)
	})

	t.Run("embedding dispatch", func(t *testing.T) {
		ctx := presentCtx(admissionTestVKValue)
		_, bifrostErr := plugin.Evaluate(ctx, &EvaluationRequest{
			RequestType: schemas.EmbeddingRequest,
			Provider:    schemas.OpenAI,
			Model:       admissionPaidModel,
		})
		require.NotNil(t, bifrostErr, "an embedding dispatch is provider work like any other")
		require.NotNil(t, bifrostErr.Type)
		assert.Equal(t, string(DecisionCreditAdmissionNotEnabled), *bifrostErr.Type)
	})
}
