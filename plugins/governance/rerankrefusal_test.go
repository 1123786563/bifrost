package governance

// The rerank admission and refusal path (11.4, #299 - the ticket's Go-side focused evidence):
// a typed rerank request that reaches the Go funnel either holds a reservation and proceeds,
// or is refused before the reserve door is asked and before any provider is dispatched.
//
// WHAT WAS BROKEN. Before this ticket the credit protocol had no rerank cell: a rerank mapped
// "unknown", the output-cap gate refused it outright (no boundable output can exist for an
// unknown call method), and `deriveBoundEvidence` had no rerank case - so even a FULLY PRICED
// rerank model was refused as cap_not_provable before the door was ever asked. Pricing the
// missing dimensions therefore could never make AC4 (补齐后正常执行) reachable.
//
// WHAT REPLACES IT. Rerank has its own capability cell (`rerank`), the ranking output is a
// structural exemption (scores and indices carry no billable output field, so there is no
// output cap to attest), and its input is counted in full over the query and every document
// and REFUSED over the deployment's ceiling rather than truncated - the documents ARE the
// payload. A door that answers price_unknown surfaces as the pricing refusal category with
// its missing-dimension worklist in the reason; a door that holds lets the same request
// through to dispatch.
//
// THE DISTINCTION THE GATE KEEPS STRAIGHT. "Unknown" is not "free". A rerank model the catalog
// prices explicitly free costs nothing and proceeds with no hold (07.2's free semantics); an
// unpriced rerank model reaches the door and is refused with the door's own reason rather than
// served on a guess - and the free case must never become the unknown case, nor the reverse.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rerankPriceUnknownAnswer is the 422 OpenBot's reserve door returns for a rerank basis whose
// billed dimensions are not priced: the closed `price_unknown` code plus the worklist 03.3's
// classification carries (one entry per missing dimension and its per-dimension reason).
const rerankPriceUnknownAnswer = `{"error":"refused","code":"price_unknown","detail":{"priceStatus":"unknown","priceReasonCode":"price_status_dims_missing","missingDimensions":[{"dimension":"input","reason":"price_dim_no_source"},{"dimension":"surcharge_per_request","reason":"price_dim_no_source"}],"snapshotVersion":7}}`

// rerankRefusalRequest is a typed rerank request for the model named, the shape the funnel
// carries: provider and model resolved, one query and two documents.
func rerankRefusalRequest(model string) *schemas.BifrostRequest {
	return rerankRefusalRequestWithDocuments(model,
		[]schemas.RerankDocument{
			{Text: "the teapot is on the table"},
			{Text: "the kettle is on the stove"},
		})
}

// rerankRefusalRequestWithDocuments is the same request with the documents the test arms.
func rerankRefusalRequestWithDocuments(model string, documents []schemas.RerankDocument) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.RerankRequest,
		RerankRequest: &schemas.BifrostRerankRequest{
			Provider:  schemas.OpenAI,
			Model:     model,
			Query:     "where is the teapot",
			Documents: documents,
		},
	}
}

// rerankRefusalAftermath asserts the refused request's accounting is empty: no reservation on
// the context, no dispatch commitment claimed, no settlement recorded - the state an attempt
// that never dispatched can leave behind, and the only one (AC3: 拒绝不产生任何账务写入).
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

// An unpriced rerank request reaches the door and is refused with the door's own pricing
// category, the reason naming THE missing dimensions so an administrator can price exactly
// them. Nothing is held, committed or dispatched (AC1-AC3).
func TestRerankRefusalRefusesUnknownPricingAndNamesTheMissingDimensions(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, rerankPriceUnknownAnswer)
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, plugin, ctx, rerankRefusalRequest(reserveUnknownModel))

	assert.Equal(t, "price_unknown", refusalReasonCode(t, bifrostErr),
		"the door's price-unknown answer surfaces as the pricing refusal category, never cap_not_provable or a pass-through")
	assert.Contains(t, bifrostErr.Error.Message, "price_unknown",
		"the refusal names the reason code OpenBot answered with, displayable to the caller")
	assert.Contains(t, bifrostErr.Error.Message, "input (price_dim_no_source)",
		"the refusal names the first missing billed dimension and why it is unpriced (AC2: 拒绝原因列出缺失维度)")
	assert.Contains(t, bifrostErr.Error.Message, "surcharge_per_request (price_dim_no_source)",
		"the refusal names every missing billed dimension, not only the first")
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
	assert.Equal(t, 1, fake.calls(), "the refusal came from the reserve door, asked exactly once")

	// The evidence the rerank call carried over the wire: the counted-in-full input bound,
	// refused over the ceiling - never truncated (the documents are the payload).
	_, body := fake.snapshot()
	require.NotNil(t, body)
	assert.Equal(t, []any{
		map[string]any{"kind": "self_enforced_input_refusal", "maximumTokens": float64(reserveTestInputCeiling)},
	}, body["boundEvidence"],
		"a rerank call attests the self-enforced input bound it was counted under; the ranking output needs no cap entry")
	assert.Equal(t, map[string]any{"provider": "openai", "model": reserveUnknownModel, "capability": "rerank"}, body["model"],
		"the reserve names the rerank capability cell (not the old unknown cell)")

	rerankRefusalAftermath(t, plugin, ctx)
}

// The same request, once the price basis completes, goes through: the door holds, the
// reservation rides the context, and nothing about the call shape changed (AC4).
func TestRerankRefusalLetsTheSameRequestThroughOnceThePriceIsKnown(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, rerankRefusalRequest(reserveUnknownModel))

	require.NoError(t, err)
	require.Nil(t, shortCircuit, "a held reservation lets the rerank request proceed to dispatch")
	reservation, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok, "the held reservation is stamped on the request context")
	assert.Equal(t, "res-reserve-1", reservation.ReservationID)
	assert.Equal(t, int64(0), reservation.OutputCapTokens,
		"a rerank call attests no output cap: the ranking output has no billable output to cap")
	assert.Nil(t, ctx.Value(governanceRejectedContextKey))
	assert.Equal(t, 1, fake.calls())
}

// An input that cannot be brought under the ceiling refuses BEFORE the door is asked: the
// documents are the payload, so the call is refused rather than sent uncounted - and a batch
// cannot smuggle documents past the counter.
func TestRerankRefusalRefusesAnInputOverTheCeilingWithoutAskingTheDoor(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	admission := admissionWithReserve(fake.endpoint(), 0)
	admission.Reserve.InputCeilingTokens = 200
	plugin := newReserveTestPlugin(t, admission)
	ctx := reserveTestCtx()

	// Fifty documents of two kilobytes each: any honest count is far over a 200-token ceiling,
	// and each document alone is not - only the batch's total is.
	documents := make([]schemas.RerankDocument, 50)
	for i := range documents {
		documents[i] = schemas.RerankDocument{Text: strings.Repeat("d", 2048)}
	}
	bifrostErr := reserveRefusalFor(t, plugin, ctx, rerankRefusalRequestWithDocuments(reserveUnknownModel, documents))

	assert.Equal(t, "cap_not_provable", refusalReasonCode(t, bifrostErr),
		"a rerank input over the ceiling is a ceiling that cannot be proven, refused as such")
	assert.Contains(t, bifrostErr.Error.Message, "ceiling",
		"the refusal names the ceiling, the caller-actionable cause")
	assert.Equal(t, 0, fake.calls(), "the door is never asked for a call whose bound already failed")

	rerankRefusalAftermath(t, plugin, ctx)
}

// A rerank request with no documents at all cannot be counted: ranking needs candidates, so
// the call is refused rather than sent uncounted - and the shape answer precedes any price.
func TestRerankRefusalRefusesAnUncountableShapeWithoutAskingTheDoor(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, plugin, ctx, rerankRefusalRequestWithDocuments(reserveUnknownModel, nil))

	assert.Equal(t, "cap_not_provable", refusalReasonCode(t, bifrostErr),
		"an uncountable rerank shape is a ceiling that cannot be proven, refused as such")
	assert.Contains(t, bifrostErr.Error.Message, "no documents",
		"the refusal names the caller-actionable shape problem")
	assert.Equal(t, 0, fake.calls(), "the door is never asked for a call whose bound already failed")

	rerankRefusalAftermath(t, plugin, ctx)
}

// The two walls that used to refuse every rerank now name its own cell and derive its own
// evidence: no output-cap record is demanded (the ranking output is a structural exemption),
// and the input bound is the self-enforced refusal.
func TestRerankRefusalHasItsOwnCreditCellAndBoundEvidence(t *testing.T) {
	assert.Equal(t, "rerank", creditCapabilityForRequestType(schemas.RerankRequest),
		"the credit protocol's closed vocabulary names the rerank cell its hold locks")

	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	assert.Empty(t, plugin.outputCapEnforcementProblem(schemas.OpenAI, reserveUnknownModel, schemas.RerankRequest),
		"the ranking output has no billable output field on the wire, so no output-cap record can exist")

	ctx := reserveTestCtx()
	evidence, problem := deriveBoundEvidence(ctx, &EvaluationRequest{
		RequestType: schemas.RerankRequest,
		Provider:    schemas.OpenAI,
		Model:       reserveUnknownModel,
		LLMRequest:  rerankRefusalRequest(reserveUnknownModel),
	}, &CreditReserveConfig{InputCeilingTokens: reserveTestInputCeiling})

	require.Empty(t, problem, "a countable rerank request carries provable bounds")
	assert.Equal(t, []CreditReserveBoundEvidence{
		{Kind: "self_enforced_input_refusal", MaximumTokens: reserveTestInputCeiling},
	}, evidence, "rerank attests only the self-enforced input bound: the ranking output needs no cap")
}

// Free and unknown stay distinct: an explicitly free rerank model is served with no hold, and
// an unpriced one reaches the door and is refused there - the same plugin shape, only the
// catalog differs.
func TestRerankRefusalKeepsExplicitlyFreeApartFromUnknown(t *testing.T) {
	freeFake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	freePlugin := newReserveTestPlugin(t, admissionWithReserve(freeFake.endpoint(), 0))
	t.Run("explicitly free rerank proceeds with no hold", func(t *testing.T) {
		ctx := reserveTestCtx()

		_, shortCircuit, err := freePlugin.PreLLMHook(ctx, rerankRefusalRequest(reserveFreeModel))

		require.NoError(t, err)
		assert.Nil(t, shortCircuit,
			"explicitly free work costs nothing: the funnel serves it without a reservation")
		assert.Nil(t, ctx.Value(CreditReservationContextKey),
			"free work reserves nothing, so there is nothing on the context to settle")
		assert.Nil(t, ctx.Value(governanceRejectedContextKey))
	})

	unknownFake := newFakeCreditService(t, http.StatusUnprocessableEntity, rerankPriceUnknownAnswer)
	unknownPlugin := newReserveTestPlugin(t, admissionWithReserve(unknownFake.endpoint(), 0))
	t.Run("unpriced rerank is refused rather than mistaken for free", func(t *testing.T) {
		ctx := reserveTestCtx()

		bifrostErr := reserveRefusalFor(t, unknownPlugin, ctx, rerankRefusalRequest(reserveUnknownModel))

		assert.Equal(t, "price_unknown", refusalReasonCode(t, bifrostErr),
			"an unpriced model is not free: the door refuses it, because its ceiling cannot be proven")
		assert.Contains(t, bifrostErr.Error.Message, "input")
		rerankRefusalAftermath(t, unknownPlugin, ctx)
	})

	assert.Equal(t, 0, freeFake.calls(),
		"explicitly free work never asks the door: it needs no hold")
	assert.Equal(t, 1, unknownFake.calls(),
		"unpriced work asks the door exactly once: the door's pricing answer is the refusal")
}
