package governance

// The per-model output-cap enforcement evidence (07.7): the record that a provider ACTUALLY
// enforces the output cap a request carries, for one model × call-method cell, with the
// verification environment and result saved so the evidence is reproducible and can be
// aggregated for conformance (22.3).
//
// WHY PER MODEL. 07.6 proved the per-request half: a call is only derived evidence when the
// request itself carries a max_tokens the provider is contractually bound to enforce. But
// "contractually bound" is still a paper claim - Q11 enables a model × call-method cell only
// when its cost ceiling is ENFORCEABLE, and interface existence is not enablement evidence.
// So the deployment records, per cell, the verification it actually performed: against which
// environment, with what result, reproducible from where. The gate below is the model-level
// half of the reserve door: a cell with no verified record is refused before the door is
// asked, whatever the request itself carries - absence of evidence is refusal, never
// enablement, and a verification that never ran is never recorded as one that did (resume
// rule: no real-provider environment means the cell stays unverified, not invented).
//
// THE EXCESS RULE (AC06). Even a verified cell is defended in depth at booking time: what
// the post hook bills can never exceed the cap the evidence attested. A provider that
// reports output beyond the cap has produced cost no reservation covers, so the excess is
// not booked as billable usage - the covered part settles, the violation is logged, and no
// unreserved cost ever enters the accounting.
//
// The tests below hold the seam to four properties, one per acceptance line:
//
//   - every dispatchable (paid, enabled) model cell has verified enforcement evidence, and
//     a cell without it is refused before the reserve door is asked, the reason visible;
//   - the record is a closed, complete structure - a record missing any verification fact,
//     or carrying an unknown mechanism or result, is not evidence and refuses the cell;
//   - unverified and not-enforced cells stay unavailable with their RECORDED reason shown,
//     which is how "we could not verify" is told apart from "we did not need to";
//   - a provider exceeding the cap books at most the capped cost, on success and on
//     billable failure alike - the excess is a violation, not a charge.

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// outputCapTestVKValue is the key value the output-cap tests' store serves.
const outputCapTestVKValue = "sk-bf-outputcap-vk"

// The rates the output-cap settlement tests price the paid model with. The output rate is
// what makes excess completion tokens measurable: a clamp that did nothing would book the
// excess at this rate and fail the exact-cost assertions below.
const (
	outputCapTestInputRate  = 0.0005
	outputCapTestOutputRate = 0.0015
)

// outputCapVerifiedRecord is a complete, valid, verified evidence record for one cell, the
// shape a deployment writes after it has actually verified enforcement in some environment.
func outputCapVerifiedRecord(provider schemas.ModelProvider, model string) OutputCapEnforcementRecord {
	return OutputCapEnforcementRecord{
		Provider:    string(provider),
		Model:       model,
		CallMethod:  "chat",
		Mechanism:   OutputCapMechanismProviderMaxTokens,
		Environment: "fake-provider-harness",
		Result:      OutputCapEvidenceVerified,
		VerifiedAt:  "2026-10-18T00:00:00Z",
		Source:      "go test ./plugins/governance -run TestOutputCap",
	}
}

// verifiedOutputCapEvidence arms every fixture model the reserve tests use, so the shared
// fixtures keep their door-level semantics (pricing stays the door's question) once the
// model-level gate is live.
func verifiedOutputCapEvidence() []OutputCapEnforcementRecord {
	return []OutputCapEnforcementRecord{
		outputCapVerifiedRecord(schemas.OpenAI, reservePaidModel),
		outputCapVerifiedRecord(schemas.OpenAI, reserveFreeModel),
		outputCapVerifiedRecord(schemas.OpenAI, reserveNegativeModel),
		outputCapVerifiedRecord(schemas.OpenAI, reserveUnknownModel),
	}
}

// admissionArmedWithEvidence copies a reserve-door admission and sets exactly the evidence
// records the test asks for, replacing whatever the base helper armed.
func admissionArmedWithEvidence(base *CreditAdmissionConfig, evidence []OutputCapEnforcementRecord) *CreditAdmissionConfig {
	base.OutputCapEvidence = evidence
	return base
}

// outputCapSettlementFixture builds a plugin whose virtual key carries a budget and a rate
// limit and whose catalog prices the paid model on BOTH sides, so what the post hook books
// can be read exactly: the budget moves by cost, the rate limit by tokens.
type outputCapSettlementFixture struct {
	plugin *GovernancePlugin
	store  GovernanceStore
}

func newOutputCapSettlementFixture(t *testing.T, admission *CreditAdmissionConfig) *outputCapSettlementFixture {
	t.Helper()
	vk := buildVirtualKeyWithProviders("vk-outputcap", outputCapTestVKValue, "outputcap-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai",
			[]string{reservePaidModel, reserveFreeModel, reserveNegativeModel, reserveUnknownModel})})
	budget := buildBudgetWithUsage("outputcap-budget", 1_000_000.0, 0.0, "1d")
	vkID := "vk-outputcap"
	budget.VirtualKeyID = &vkID
	vk.Budgets = append(vk.Budgets, *budget)
	rl := buildRateLimit("outputcap-rl", 1_000_000, 1_000_000)
	vk.RateLimit = rl
	rlID := rl.ID
	vk.RateLimitID = &rlID

	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
		Budgets:     []configstoreTables.TableBudget{*budget},
		RateLimits:  []configstoreTables.TableRateLimit{*rl},
	}, nil, &mockInMemoryStore{})
	require.NoError(t, err)

	inRate, outRate := outputCapTestInputRate, outputCapTestOutputRate
	ds := datasheet.NewTestStore(nil)
	ds.SetPricingRowsForTest([]configstoreTables.TableModelPricing{
		{Model: reservePaidModel, Provider: string(schemas.OpenAI), Mode: "chat",
			InputCostPerToken: &inRate, OutputCostPerToken: &outRate},
	})
	catalog := modelcatalog.NewTestCatalogWithDatasheet(ds)

	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory:          boolPtr(false),
		OpenBotCreditAdmission: admission,
	}, logger, store, nil, catalog, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return &outputCapSettlementFixture{plugin: plugin, store: store}
}

func (f *outputCapSettlementFixture) budgetUsage() float64 {
	return f.store.GetGovernanceData(context.Background()).Budgets["outputcap-budget"].CurrentUsage
}

func (f *outputCapSettlementFixture) tokens() int64 {
	return f.store.GetGovernanceData(context.Background()).RateLimits["outputcap-rl"].TokenCurrentUsage
}

// outputCapCtx is the request context the way the pipeline carries one into PreLLMHook.
func outputCapCtx() *schemas.BifrostContext {
	ctx := presentCtx(outputCapTestVKValue)
	ctx.SetValue(schemas.BifrostContextKeyRequestID, "req-outputcap-1")
	ctx.SetValue(schemas.BifrostContextKeyFallbackIndex, 0)
	return ctx
}

// --- AC2: a model without verified enforcement evidence is not callable ---------------------

func TestOutputCapGateRefusesModelWithoutEvidence(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())

	t.Run("no evidence records at all", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0), nil))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit,
			"a model whose provider was never verified to enforce the output cap is not dispatched")
		require.NotNil(t, shortCircuit.Error.Error)
		require.NotNil(t, shortCircuit.Error.Error.Code)
		assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code,
			"an unverified cap is a cap_not_provable refusal")
		assert.Contains(t, shortCircuit.Error.Error.Message, reservePaidModel,
			"the refusal names the model it refuses")
		assert.Contains(t, shortCircuit.Error.Error.Message, "output-cap enforcement evidence",
			"the refusal says what is missing")
		assert.NotContains(t, shortCircuit.Error.Error.Message, reserveServiceSecret,
			"the refusal never carries the trusted service credential")
		assert.Equal(t, 0, fake.calls(),
			"the door is never asked for a cell whose enforcement is unverified")
		assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
			"a refused request is marked refused for everything downstream")
	})

	t.Run("evidence for other models only", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0),
			[]OutputCapEnforcementRecord{outputCapVerifiedRecord(schemas.OpenAI, "some-other-model")}))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit, "evidence is per cell: another model's record verifies nothing")
		require.NotNil(t, shortCircuit.Error.Error)
		assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code)
		assert.Equal(t, 0, fake.calls())
	})

	t.Run("evidence for another call method only", func(t *testing.T) {
		otherMethod := outputCapVerifiedRecord(schemas.OpenAI, reservePaidModel)
		otherMethod.CallMethod = "embedding"
		plugin := newReserveTestPlugin(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0),
			[]OutputCapEnforcementRecord{otherMethod}))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit,
			"a cell is model × call method: verification under one call method does not enable another")
		require.NotNil(t, shortCircuit.Error.Error)
		assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code)
		assert.Equal(t, 0, fake.calls())
	})

	t.Run("call method the vocabulary cannot bound", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0),
			verifiedOutputCapEvidence()))
		problem := plugin.outputCapEnforcementProblem(schemas.OpenAI, reservePaidModel, schemas.BatchResultsRequest)
		assert.NotEmpty(t, problem,
			"a dispatch the capability vocabulary cannot map has no boundable output side")
		assert.Contains(t, problem, "call method", "the problem names the cause")
	})
}

// A verified cell dispatches, the door is asked, and the reservation carries the attested
// cap so the post hook can hold booking to it.
func TestOutputCapVerifiedCellDispatchesAndCarriesTheCap(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0),
		verifiedOutputCapEvidence()))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "a verified cell is enabled for the call method it was verified under")
	assert.Equal(t, 1, fake.calls(), "the verified cell reaches the reserve door")

	reservation, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok, "a held reservation is stamped on the request context")
	assert.Equal(t, int64(1024), reservation.OutputCapTokens,
		"the reservation carries the attested output cap the request itself declared")
}

// --- AC3: the record is a closed, complete structure ----------------------------------------

func TestOutputCapEvidenceRecordStructureIsClosedAndComplete(t *testing.T) {
	valid := outputCapVerifiedRecord(schemas.OpenAI, reservePaidModel)
	assert.Empty(t, valid.evidenceProblem(), "a complete verified record is usable evidence")

	cut := func(mutate func(*OutputCapEnforcementRecord)) OutputCapEnforcementRecord {
		record := outputCapVerifiedRecord(schemas.OpenAI, reservePaidModel)
		mutate(&record)
		return record
	}
	for name, record := range map[string]OutputCapEnforcementRecord{
		"no provider":    cut(func(r *OutputCapEnforcementRecord) { r.Provider = "" }),
		"no model":       cut(func(r *OutputCapEnforcementRecord) { r.Model = "" }),
		"no call method": cut(func(r *OutputCapEnforcementRecord) { r.CallMethod = "" }),
		"no mechanism":   cut(func(r *OutputCapEnforcementRecord) { r.Mechanism = "" }),
		"no environment": cut(func(r *OutputCapEnforcementRecord) { r.Environment = "" }),
		"no result":      cut(func(r *OutputCapEnforcementRecord) { r.Result = "" }),
		"no verified at": cut(func(r *OutputCapEnforcementRecord) { r.VerifiedAt = "" }),
		"no source":      cut(func(r *OutputCapEnforcementRecord) { r.Source = "" }),
		"no detail":      cut(func(r *OutputCapEnforcementRecord) { r.Result = OutputCapEvidenceUnverified; r.Detail = "" }),
		"unknown result": cut(func(r *OutputCapEnforcementRecord) { r.Result = "looks-fine" }),
		"unknown mechanism": cut(func(r *OutputCapEnforcementRecord) {
			r.Mechanism = "vendor_promises_it"
		}),
	} {
		assert.NotEmpty(t, record.evidenceProblem(), "%s: a record missing a verification fact is not evidence", name)
	}
}

// The structured evidence is saved whole - model, call method, environment, result, the
// reproducibility facts - so 22.3 can aggregate what was verified where, and the snapshot
// reflects the live configuration.
func TestOutputCapEvidenceSnapshotCarriesVerificationFacts(t *testing.T) {
	records := []OutputCapEnforcementRecord{
		outputCapVerifiedRecord(schemas.OpenAI, reservePaidModel),
		{Provider: string(schemas.OpenAI), Model: "unverified-model", CallMethod: "chat",
			Mechanism: OutputCapMechanismProviderMaxTokens, Environment: "staging",
			Result: OutputCapEvidenceUnverified, Detail: "no real provider credentials available",
			VerifiedAt: "2026-10-18T01:00:00Z", Source: "manual attempt log"},
	}
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0), records))

	snapshot := plugin.OutputCapEvidenceSnapshot()
	require.Len(t, snapshot, len(records), "every recorded cell is exported")
	for i, record := range records {
		assert.Equal(t, record, snapshot[i],
			"the snapshot carries the record whole: model, call method, environment, result, detail, verified-at, source")
	}

	empty := newReserveTestPlugin(t, enabledAdmission())
	assert.Empty(t, empty.OutputCapEvidenceSnapshot(),
		"a deployment that recorded nothing exports nothing: absence is not dressed up as evidence")
}

// --- AC2: unverified and not-enforced cells stay unavailable, reason recorded ----------------

func TestOutputCapEvidenceUnverifiedAndFailedCellsAreRefusedWithRecordedReason(t *testing.T) {
	unverified := outputCapVerifiedRecord(schemas.OpenAI, reservePaidModel)
	unverified.Result = OutputCapEvidenceUnverified
	unverified.Detail = "no real provider credentials available"

	notEnforced := outputCapVerifiedRecord(schemas.OpenAI, reservePaidModel)
	notEnforced.Result = OutputCapEvidenceNotEnforced
	notEnforced.Detail = "provider ignored max_tokens in staging verification"

	for name, record := range map[string]OutputCapEnforcementRecord{
		"unverified":   unverified,
		"not_enforced": notEnforced,
	} {
		t.Run(name, func(t *testing.T) {
			fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
			plugin := newReserveTestPlugin(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0),
				[]OutputCapEnforcementRecord{record}))
			ctx := reserveTestCtx()

			_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
			require.NoError(t, err)
			require.NotNil(t, shortCircuit,
				"a cell whose verification did not conclude in enforcement is not enabled")
			require.NotNil(t, shortCircuit.Error.Error)
			assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code)
			assert.Contains(t, shortCircuit.Error.Error.Message, record.Detail,
				"the recorded reason is shown, not swallowed")
			assert.Contains(t, shortCircuit.Error.Error.Message, record.Environment,
				"the refusal says where the verification was attempted")
			assert.Equal(t, 0, fake.calls())
		})
	}
}

// --- AC4: a provider exceeding the cap books no unreserved cost ------------------------------

// outputCapChatResponse is a fake provider's answer: a chat response whose usage the test
// controls, the shape the post hook accounts.
func outputCapChatResponse(prompt, completion int, cost *float64) *schemas.BifrostResponse {
	resp := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{
			Model: reservePaidModel,
			Usage: &schemas.BifrostLLMUsage{
				PromptTokens:     prompt,
				CompletionTokens: completion,
				TotalTokens:      prompt + completion,
			},
		},
	}
	if cost != nil {
		resp.ChatResponse.Usage.Cost = &schemas.BifrostCost{TotalCost: *cost}
	}
	resp.PopulateExtraFields(schemas.ChatCompletionRequest, schemas.OpenAI, reservePaidModel, reservePaidModel)
	return resp
}

func TestOutputCapExcessUsageIsNotBookedBeyondTheCap(t *testing.T) {
	const maxTokens = 1024
	// input 100 x 0.0005 = 0.05; capped output 1024 x 0.0015 = 1.536.
	const cappedCost = 100*outputCapTestInputRate + maxTokens*outputCapTestOutputRate
	const cappedTokens = 100 + maxTokens

	t.Run("provider stays under the cap: full usage books", func(t *testing.T) {
		fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		f := newOutputCapSettlementFixture(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0),
			verifiedOutputCapEvidence()))
		ctx := outputCapCtx()
		req := reserveChatRequest(reservePaidModel)
		_, shortCircuit, err := f.plugin.PreLLMHook(ctx, req)
		require.NoError(t, err)
		require.Nil(t, shortCircuit)

		_, _, hookErr := f.plugin.PostLLMHook(ctx, outputCapChatResponse(100, 400, nil), nil)
		require.NoError(t, hookErr)
		require.Eventually(t, func() bool { return f.budgetUsage() > 0 }, 2*time.Second, 10*time.Millisecond,
			"the control arm settles")

		assert.InDelta(t, 100*outputCapTestInputRate+400*outputCapTestOutputRate, f.budgetUsage(), 1e-9,
			"honest usage under the cap books in full - the clamp never under-bills")
		assert.Equal(t, int64(500), f.tokens())
	})

	t.Run("provider exceeds the cap: only the capped cost books", func(t *testing.T) {
		fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		f := newOutputCapSettlementFixture(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0),
			verifiedOutputCapEvidence()))
		ctx := outputCapCtx()
		req := reserveChatRequest(reservePaidModel)
		_, shortCircuit, err := f.plugin.PreLLMHook(ctx, req)
		require.NoError(t, err)
		require.Nil(t, shortCircuit)

		// The provider ignored the cap: 5000 completion tokens beyond what the evidence
		// attested. The excess was never reserved, so none of it may book.
		_, _, hookErr := f.plugin.PostLLMHook(ctx, outputCapChatResponse(100, maxTokens+5000, nil), nil)
		require.NoError(t, hookErr)
		require.Eventually(t, func() bool { return f.budgetUsage() > 0 }, 2*time.Second, 10*time.Millisecond)

		assert.InDelta(t, cappedCost, f.budgetUsage(), 1e-9,
			"booking stops at the attested cap: the excess is not billed as unreserved cost")
		assert.Equal(t, int64(cappedTokens), f.tokens(),
			"the token accounting holds to the cap too")
		assert.Less(t, f.budgetUsage(), 100*outputCapTestInputRate+float64(maxTokens+5000)*outputCapTestOutputRate,
			"the unclamped cost would have been strictly higher, so the clamp is what held")
	})

	t.Run("provider-reported cost cannot smuggle the excess in", func(t *testing.T) {
		fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		f := newOutputCapSettlementFixture(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0),
			verifiedOutputCapEvidence()))
		ctx := outputCapCtx()
		req := reserveChatRequest(reservePaidModel)
		_, shortCircuit, err := f.plugin.PreLLMHook(ctx, req)
		require.NoError(t, err)
		require.Nil(t, shortCircuit)

		inflated := 999.0
		_, _, hookErr := f.plugin.PostLLMHook(ctx, outputCapChatResponse(100, maxTokens+5000, &inflated), nil)
		require.NoError(t, hookErr)
		require.Eventually(t, func() bool { return f.budgetUsage() > 0 }, 2*time.Second, 10*time.Millisecond)

		assert.InDelta(t, cappedCost, f.budgetUsage(), 1e-9,
			"a violating response's own cost figure is part of the violation: cost recomputes from the capped tokens")
	})

	t.Run("billable failure usage above the cap is clamped too", func(t *testing.T) {
		fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
		f := newOutputCapSettlementFixture(t, admissionArmedWithEvidence(admissionWithReserve(fake.endpoint(), 0),
			verifiedOutputCapEvidence()))
		ctx := outputCapCtx()
		req := reserveChatRequest(reservePaidModel)
		_, shortCircuit, err := f.plugin.PreLLMHook(ctx, req)
		require.NoError(t, err)
		require.Nil(t, shortCircuit)

		billedErr := &schemas.BifrostError{
			ExtraFields: schemas.BifrostErrorExtraFields{
				RequestType:            schemas.ChatCompletionRequest,
				Provider:               schemas.OpenAI,
				OriginalModelRequested: reservePaidModel,
				BilledUsage: &schemas.BifrostLLMUsage{
					PromptTokens:     100,
					CompletionTokens: maxTokens + 5000,
					TotalTokens:      100 + maxTokens + 5000,
				},
			},
		}
		_, _, hookErr := f.plugin.PostLLMHook(ctx, nil, billedErr)
		require.NoError(t, hookErr)
		require.Eventually(t, func() bool { return f.budgetUsage() > 0 }, 2*time.Second, 10*time.Millisecond)

		assert.InDelta(t, cappedCost, f.budgetUsage(), 1e-9,
			"a billable failure books at most what the cap covers, the same as a success")
		assert.Equal(t, int64(cappedTokens), f.tokens())
	})
}
