package governance

// The embedding refusal path (Bifrost 11.2, #297): an embeddings request whose price basis
// is unknown is refused BEFORE the provider is reached, with the refusal that names pricing,
// and a price basis that completes lets the same request through.
//
// THE ENTRY. An embeddings dispatch enters the same Evaluate funnel as chat: the credit
// gate asks the reserve door, and the door's 422 price_unknown — the answer OpenBot's price
// snapshot gives a model whose dims do not price completely — must surface as the
// price-unknown refusal category (07.3), never as cap_not_provable or a pass-through. What
// stands between an embeddings request and that door question is the evidence half of
// admission (07.6/07.7), and the vector output is the whole answer there: the embeddings
// wire's response usage carries prompt tokens only — no field an output price could
// multiply (11.1's evidence) — so the call has no output cap to attest and no cap record to
// demand, and its input is counted in full and refused over the ceiling rather than
// truncated, because an embeddings batch's entries ARE the payload: dropping one changes
// the answer the caller asked for.
//
// THE ACCOUNTING. A refused request holds nothing, commits to nothing, settles nothing:
// the assertions read the plugin's own commitment and outbox stores, which is the
// plugin-level face of "供应商调用数为 0" — the pre-hook short circuit is the pipeline's
// only exit (core returns it without dispatching; AllowFallbacks=false keeps every other
// layer from retrying it anywhere), so a store that is empty is the only state an
// un-dispatched attempt can leave behind.

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/configstore"
	configstoreTables "github.com/maximhq/bifrost/framework/configstore/tables"
	"github.com/maximhq/bifrost/framework/modelcatalog"
	"github.com/maximhq/bifrost/framework/modelcatalog/datasheet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// embeddingRefusalModel is the fixture embeddings model: absent from the catalog's pricing
// rows, so its pricing is unknown to the gateway and the reserve door — where OpenBot's
// price snapshot is the authority — answers for it.
const embeddingRefusalModel = "text-embedding-refusal-3"

// embeddingRefusalInputCeiling is the input ceiling the happy-path fixtures arm: generous
// for a small batch, and the one boundary the over-ceiling test shrinks.
const embeddingRefusalInputCeiling = int64(60000)

// newEmbeddingRefusalPlugin arms the admission extension for the embeddings fixture model:
// a reserve door pointed at the fake, NO output-cap enforcement records at all (the
// embeddings wire has no output cap to verify - the vector output's evidence is
// structural), and a virtual key that carries the fixture model, so a held reservation
// lets the request through access as well (the door's own semantics, one gate downstream).
func newEmbeddingRefusalPlugin(t *testing.T, admission *CreditAdmissionConfig) *GovernancePlugin {
	t.Helper()
	vk := buildVirtualKeyWithProviders("vk-embedding-refusal", reserveTestVKValue, "embedding-refusal-vk",
		[]configstoreTables.TableVirtualKeyProviderConfig{buildProviderConfig("openai",
			[]string{embeddingRefusalModel})})
	logger := NewMockLogger()
	store, err := NewLocalGovernanceStore(context.Background(), logger, nil, &configstore.GovernanceConfig{
		VirtualKeys: []configstoreTables.TableVirtualKey{*vk},
	}, nil, &mockInMemoryStore{})
	require.NoError(t, err)

	// No pricing rows: the fixture model's pricing is unknown to the gateway's catalog, so
	// the reserve door - where OpenBot's price snapshot is the authority - answers for it.
	ds := datasheet.NewTestStore(nil)
	catalog := modelcatalog.NewTestCatalogWithDatasheet(ds)

	plugin, err := InitFromStore(context.Background(), &Config{
		IsVkMandatory:          boolPtr(false),
		OpenBotCreditAdmission: admission,
	}, logger, store, nil, catalog, nil, nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, plugin.Cleanup()) })
	return plugin
}

// embeddingRefusalAdmission arms a reserve door with the fixture's input ceiling and no
// output-cap records (see newEmbeddingRefusalPlugin).
func embeddingRefusalAdmission(endpoint string, inputCeiling int64) *CreditAdmissionConfig {
	enabled := true
	return &CreditAdmissionConfig{
		Enabled: &enabled,
		Reserve: &CreditReserveConfig{
			Endpoint:           endpoint,
			ServiceSecret:      reserveServiceSecret,
			GrantID:            "grant-embedding-refusal-1",
			PayerKind:          "personal",
			PayerID:            "user-embedding-refusal-1",
			SnapshotVersion:    11,
			RateVersion:        2,
			InputCeilingTokens: inputCeiling,
			MaximumCredits:     500,
		},
	}
}

// embeddingRefusalRequest is an embeddings request for the fixture model: entries of plain
// text, the shape the funnel carries to the provider.
func embeddingRefusalRequest(entryText ...string) *schemas.BifrostRequest {
	input := make([]schemas.EmbeddingInputItem, 0, len(entryText))
	for _, text := range entryText {
		value := text
		input = append(input, schemas.EmbeddingInputItem{
			Content: schemas.EmbeddingContent{
				{Type: schemas.EmbeddingContentPartTypeText, Text: &value},
			},
		})
	}
	return &schemas.BifrostRequest{
		RequestType:      schemas.EmbeddingRequest,
		EmbeddingRequest: &schemas.BifrostEmbeddingRequest{Provider: schemas.OpenAI, Model: embeddingRefusalModel, Input: input},
	}
}

// embeddingRefusalAftermath asserts the refused request's accounting is empty: no
// reservation on the context, no dispatch commitment claimed, no settlement recorded. That
// is the state an attempt that never dispatched can leave behind — and the only one.
func embeddingRefusalAftermath(t *testing.T, plugin *GovernancePlugin, ctx *schemas.BifrostContext) {
	t.Helper()
	assert.Nil(t, ctx.Value(CreditReservationContextKey),
		"a refused embeddings request holds no reservation: there is nothing to settle against")
	pending, err := plugin.dispatchCommitments.ListPending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, pending, "a refused embeddings request claims no dispatch commitment: nothing was dispatched")
	records, err := plugin.settlementOutbox.Pending(context.Background())
	require.NoError(t, err)
	assert.Empty(t, records, "a refused embeddings request records no settlement: no usage was produced")
}

// --- the refusal the ticket names --------------------------------------------------------

// An embeddings request whose price basis is unknown is refused with the pricing category,
// before any provider is reached, holding and committing nothing.
func TestEmbeddingRefusalRefusesUnknownPricingBeforeTheProviderIsReached(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	plugin := newEmbeddingRefusalPlugin(t, embeddingRefusalAdmission(fake.endpoint(), embeddingRefusalInputCeiling))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, plugin, ctx, embeddingRefusalRequest("hello", "world"))

	assert.Equal(t, "price_unknown", refusalReasonCode(t, bifrostErr),
		"the door's price-unknown answer surfaces as the pricing refusal category, never cap_not_provable or a pass-through")
	assert.Contains(t, bifrostErr.Error.Message, "price_unknown",
		"the refusal names the reason code OpenBot answered with, displayable to the caller")
	require.NotNil(t, bifrostErr.Type)
	assert.Equal(t, string(DecisionCreditReserveNotHeld), *bifrostErr.Type)
	assert.Equal(t, 1, fake.calls(), "the refusal came from the reserve door, asked exactly once")

	// The evidence the embeddings call carried over the wire: the counted-in-full input
	// bound, refused over the ceiling — never truncated (the entries are the payload).
	_, body := fake.snapshot()
	require.NotNil(t, body)
	assert.Equal(t, []any{
		map[string]any{"kind": "self_enforced_input_refusal", "maximumTokens": float64(embeddingRefusalInputCeiling)},
	}, body["boundEvidence"],
		"an embeddings call attests the self-enforced input bound it was counted under; the vector output needs no cap entry")
	assert.Equal(t, map[string]any{"provider": "openai", "model": embeddingRefusalModel, "capability": "embedding"}, body["model"],
		"the reserve names the embedding capability cell")

	embeddingRefusalAftermath(t, plugin, ctx)
}

// The same request, once the price basis completes, goes through: the door holds, the
// reservation rides the context, and nothing about the call shape changed.
func TestEmbeddingRefusalLetsTheSameRequestThroughOnceThePriceIsKnown(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newEmbeddingRefusalPlugin(t, embeddingRefusalAdmission(fake.endpoint(), embeddingRefusalInputCeiling))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, embeddingRefusalRequest("hello", "world"))

	require.NoError(t, err)
	require.Nil(t, shortCircuit, "a held reservation lets the embeddings request proceed to dispatch")
	reservation, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok, "the held reservation is stamped on the request context")
	assert.Equal(t, "res-reserve-1", reservation.ReservationID)
	assert.Equal(t, int64(0), reservation.OutputCapTokens,
		"an embeddings call attests no output cap: the vector output has no billable output to cap")
	assert.Nil(t, ctx.Value(governanceRejectedContextKey))
}

// An input that cannot be brought under the ceiling refuses BEFORE the door is asked: the
// entries are the payload, so the call is refused rather than sent uncounted — and a batch
// cannot smuggle entries past the counter.
func TestEmbeddingRefusalRefusesAnInputOverTheCeilingWithoutAskingTheDoor(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newEmbeddingRefusalPlugin(t, embeddingRefusalAdmission(fake.endpoint(), 200))
	ctx := reserveTestCtx()

	// Fifty entries of two kilobytes each: any honest count is far over a 200-token
	// ceiling, and each entry alone is not — only the batch's total is.
	entries := make([]string, 50)
	for i := range entries {
		entries[i] = strings.Repeat("e", 2048)
	}
	bifrostErr := reserveRefusalFor(t, plugin, ctx, embeddingRefusalRequest(entries...))

	assert.Equal(t, "cap_not_provable", refusalReasonCode(t, bifrostErr),
		"an embeddings input over the ceiling is a ceiling that cannot be proven, refused as such")
	assert.Contains(t, bifrostErr.Error.Message, "ceiling",
		"the refusal names the ceiling, the caller-actionable cause")
	assert.Equal(t, 0, fake.calls(), "the door is never asked for a call whose bound already failed")

	embeddingRefusalAftermath(t, plugin, ctx)
}

// The embeddings call method needs no output-cap enforcement record: the vector output is
// structural evidence (no billable output field on the wire), so demanding a chat-style
// max_tokens verification would be demanding evidence of a cap that cannot exist.
func TestEmbeddingRefusalNeedsNoOutputCapRecordForTheVectorOutput(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusUnprocessableEntity, refusedAnswer("price_unknown"))
	// The admission carries no output-cap records at all; a chat call under this config is
	// refused by the 07.7 gate (pinned in outputcapevidence_test.go), and the embeddings
	// call below still reaches the door — its refusal is the door's price answer.
	plugin := newEmbeddingRefusalPlugin(t, embeddingRefusalAdmission(fake.endpoint(), embeddingRefusalInputCeiling))
	ctx := reserveTestCtx()

	bifrostErr := reserveRefusalFor(t, plugin, ctx, embeddingRefusalRequest("hello"))

	assert.Equal(t, "price_unknown", refusalReasonCode(t, bifrostErr),
		"the embeddings call reached the door and was refused by pricing, not by the output-cap gate")
	assert.Equal(t, 1, fake.calls())
}

// A raw request body — bytes the gateway can neither count in full nor attest — refuses
// the same way every other call shape does.
func TestEmbeddingRefusalRefusesARawRequestBody(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newEmbeddingRefusalPlugin(t, embeddingRefusalAdmission(fake.endpoint(), embeddingRefusalInputCeiling))
	ctx := reserveTestCtx()
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)

	bifrostErr := reserveRefusalFor(t, plugin, ctx, embeddingRefusalRequest("hello"))

	assert.Equal(t, "cap_not_provable", refusalReasonCode(t, bifrostErr),
		"a raw body is a shape the gateway can neither count in full nor attest")
	assert.Contains(t, bifrostErr.Error.Message, "raw")
	assert.Equal(t, 0, fake.calls())
}
