package governance

// The rerank refusal path (11.4, #299 - the ticket's Go-side focused evidence): a typed rerank
// request that reaches the Go funnel cannot be admitted, and is refused before the reserve door
// is asked and before any provider is dispatched. The behaviour has been the funnel's since the
// gate landed; what these tests add is the focused Go proof the ticket's verification names.
//
// WHY IT CANNOT PASS SILENTLY. Two walls stand between a rerank call and a reservation, and
// both are fail-closed by construction:
//
//  1. creditCapabilityForRequestType has no rerank cell in the credit protocol's closed
//     vocabulary, so it maps to "unknown" - and outputCapEnforcementProblem refuses an unknown
//     call method outright, because no boundable output can exist for it.
//  2. If that gate ever stopped refusing, deriveBoundEvidence derives evidence only for chat
//     and embeddings, so a rerank call is refused as a ceiling that cannot be proven.
//
// Both endings are cap_not_provable, and neither reaches reserveBeforeDispatch's door call: a
// refusal here is zero door calls, zero dispatch, zero accounting.
//
// THE DISTINCTION THE GATE KEEPS STRAIGHT. "Unknown" is not "free". A rerank model the catalog
// prices explicitly free costs nothing and proceeds with no hold (07.2's free semantics); an
// unpriced rerank model is refused as unprovable rather than served on a guess - and the free
// case must never become the unknown case, nor the reverse.

import (
	"context"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rerankRefusalRequest is a typed rerank request for the model named, the shape the funnel
// carries: provider and model resolved, one query and two documents.
func rerankRefusalRequest(model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.RerankRequest,
		RerankRequest: &schemas.BifrostRerankRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Query:    "where is the teapot",
			Documents: []schemas.RerankDocument{
				{Text: "the teapot is on the table"},
				{Text: "the kettle is on the stove"},
			},
		},
	}
}

// rerankRefusalAftermath asserts the refused request's accounting is empty: no reservation on
// the context, no dispatch commitment claimed, no settlement recorded - the state an attempt
// that never dispatched can leave behind, and the only one.
func rerankRefusalAftermath(t *testing.T, plugin *GovernancePlugin, ctx *schemas.BifrostContext) {
	t.Helper()
	assert.Nil(t, ctx.Value(CreditReservationContextKey),
		"a refused rerank request holds no reservation: there is nothing to settle against")
	pending, err := plugin.dispatchCommitments.ListPending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending, "a refused rerank request claims no dispatch commitment: nothing was dispatched")
	records, err := plugin.settlementOutbox.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, records, "a refused rerank request records no settlement: no usage was produced")
}

// An unpriced rerank request is refused as cap_not_provable before the reserve door is asked:
// the call method has no credit cell and no provable ceiling, so nothing is held, committed or
// dispatched.
func TestRerankRefusalRefusesAnUnpricedRerankBeforeTheReserveDoor(t *testing.T) {
	// A door armed to hold anything: the test's point is that it is never asked.
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, plugin, ctx, rerankRefusalRequest(reserveUnknownModel))

	assert.Equal(t, "cap_not_provable", refusalReasonCode(t, bifrostErr),
		"an unprovable ceiling is refused as such, never passed through and never priced by guess")
	assert.Contains(t, bifrostErr.Error.Message, "no boundable output",
		"the refusal names the cause a caller can act on: the call method has no boundable output")
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
	assert.Equal(t, 0, fake.calls(), "the door is never asked for a call whose ceiling cannot be proven")

	rerankRefusalAftermath(t, plugin, ctx)
}

// Free and unknown stay distinct: an explicitly free rerank model is served with no hold, and
// an unpriced one is refused - the same plugin, the same call shape, only the catalog differs.
func TestRerankRefusalKeepsExplicitlyFreeApartFromUnknown(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))

	t.Run("explicitly free rerank proceeds with no hold", func(t *testing.T) {
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, rerankRefusalRequest(reserveFreeModel))

		require.NoError(t, err)
		assert.Nil(t, shortCircuit,
			"explicitly free work costs nothing: the funnel serves it without a reservation")
		assert.Nil(t, ctx.Value(CreditReservationContextKey),
			"free work reserves nothing, so there is nothing on the context to settle")
		assert.Nil(t, ctx.Value(governanceRejectedContextKey))
	})

	t.Run("unpriced rerank is refused rather than mistaken for free", func(t *testing.T) {
		ctx := reserveTestCtx()

		bifrostErr := reserveRefusalFor(t, plugin, ctx, rerankRefusalRequest(reserveUnknownModel))

		assert.Equal(t, "cap_not_provable", refusalReasonCode(t, bifrostErr),
			"an unpriced model is not free: it is refused, because its ceiling cannot be proven")
		rerankRefusalAftermath(t, plugin, ctx)
	})

	assert.Equal(t, 0, fake.calls(),
		"neither free nor unpriced rerank work asks the door: one needs no hold, the other cannot be held")
}

// The two walls behind the refusal, pinned where they live: rerank has no credit cell in the
// closed vocabulary, and no evidence derivation - so the funnel's refusal is structural rather
// than an accident of configuration.
func TestRerankRefusalHasNoCreditCellAndNoBoundEvidence(t *testing.T) {
	assert.Equal(t, "unknown", creditCapabilityForRequestType(schemas.RerankRequest),
		"the credit protocol's closed vocabulary has no rerank cell, so the capability maps to unknown")

	ctx := reserveTestCtx()
	evidence, problem := deriveBoundEvidence(ctx, &EvaluationRequest{
		RequestType: schemas.RerankRequest,
		Provider:    schemas.OpenAI,
		Model:       reserveUnknownModel,
		LLMRequest:  rerankRefusalRequest(reserveUnknownModel),
	}, &CreditReserveConfig{InputCeilingTokens: reserveTestInputCeiling})

	assert.Empty(t, evidence,
		"no evidence may be invented for a call method the derivation does not know")
	assert.Equal(t, "this call type has no provable cost-ceiling evidence yet", problem,
		"the derivation refuses the call method rather than estimating a ceiling (ADR 0003)")
}
