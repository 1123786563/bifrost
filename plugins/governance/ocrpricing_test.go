package governance

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

/*
 * The OCR capability's gateway half (11.6, #301): an OCR dispatch names the OCR capability
 * cell — the cell OpenBot's price snapshots price and, when a billed dimension is unknown,
 * REFUSE — and the funnel's own posture for the OCR call method is pinned with it.
 *
 * The mapping is the load-bearing line. Before it, an OCR request mapped capability
 * "unknown": a cell no administrator can open, so even a fully priced OCR model could never
 * be admitted — while an unknown-priced one was refused for the WRONG reason (no cell)
 * rather than the door's price_unknown. With the mapping, the reserve payload carries the
 * capability the price basis prices; unknown pricing stays exactly where it belongs — the
 * door's own refusal, never a free pass, never a mapping accident.
 *
 * The call method's own evidence is still future work (11.5's output cap is caller-attested
 * on the server side; the gateway-side evidence derivation covers chat only), so an OCR
 * request through the funnel is refused BEFORE the door with cap_not_provable — the honest
 * "path not enabled yet" posture — which already satisfies the ticket's provider-call
 * guarantee: no OCR request without provable bounds reaches any provider, whatever its
 * price basis says. Explicitly free OCR models cost nothing to serve and skip the door,
 * exactly as free chat models do.
 */

func TestOcrPricingCapabilityMappingNamesTheOcrCell(t *testing.T) {
	assert.Equal(t, "ocr", creditCapabilityForRequestType(schemas.OCRRequest),
		"an OCR dispatch names the OCR capability cell its hold locks")
	// The neighbours are unchanged — the mapping adds the cell, it does not reshuffle the
	// vocabulary.
	assert.Equal(t, "chat", creditCapabilityForRequestType(schemas.ChatCompletionRequest))
	assert.Equal(t, "embedding", creditCapabilityForRequestType(schemas.EmbeddingRequest))
	assert.Equal(t, "realtime", creditCapabilityForRequestType(schemas.RealtimeRequest))
	assert.Equal(t, "unknown", creditCapabilityForRequestType(schemas.PassthroughRequest))
}

func TestOcrPricingReservePayloadCarriesTheOcrCapability(t *testing.T) {
	reserve := &CreditReserveConfig{
		GrantID:            "grant-ocr-1",
		PayerKind:          "personal",
		PayerID:            "user-ocr-1",
		SnapshotVersion:    9,
		RateVersion:        4,
		InputCeilingTokens: 1000,
		MaximumCredits:     250,
	}
	payload, err := json.Marshal(reserve.requestFor(&EvaluationRequest{
		RequestType: schemas.OCRRequest,
		Provider:    schemas.OpenAI,
		Model:       "ocr-priced-model",
	}, "att-ocr-payload", []CreditReserveBoundEvidence{
		{Kind: "provider_max_tokens", MaximumTokens: 20000},
	}))
	require.NoError(t, err)

	var request struct {
		IdempotencyKey string `json:"idempotencyKey"`
		Model          struct {
			Provider   string `json:"provider"`
			Model      string `json:"model"`
			Capability string `json:"capability"`
		} `json:"model"`
	}
	require.NoError(t, json.Unmarshal(payload, &request))
	assert.Equal(t, "ocr", request.Model.Capability,
		"the door prices and refuses the cell the request names — never a mapping accident")
	assert.Equal(t, "ocr-priced-model", request.Model.Model)
	assert.Equal(t, "att-ocr-payload", request.IdempotencyKey)
}

func TestOcrPricingUnknownOcrModelIsRefusedWithNoProviderCall(t *testing.T) {
	// A door that would hold anything — the test's point is that it is never asked: the
	// OCR call method has no gateway-side bound evidence yet, so the funnel refuses the
	// request before the door, and the provider is called by no one.
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()
	req := &schemas.BifrostRequest{
		RequestType: schemas.OCRRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    reserveUnknownModel,
		},
	}

	bifrostErr := reserveRefusalFor(t, plugin, ctx, req)
	// The first honest refusal the funnel reaches is the model-cell output-cap gate (07.7):
	// no verified evidence that the provider enforces an OCR output cap, so the model is
	// not enabled — the call method's bounds are not provable on the gateway yet.
	assert.Contains(t, bifrostErr.Error.Message, "output-cap enforcement evidence",
		"the honest refusal: the OCR call method's bounds are not provable on the gateway yet")
	assert.Equal(t, 0, fake.calls(),
		"the door is never asked — and no provider call follows a refusal")
}

func TestOcrPricingExplicitlyFreeOcrModelSkipsTheDoor(t *testing.T) {
	fake := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
	ctx := reserveTestCtx()
	req := &schemas.BifrostRequest{
		RequestType: schemas.OCRRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    reserveFreeModel,
		},
	}

	_, shortCircuit, err := plugin.PreLLMHook(ctx, req)
	require.NoError(t, err)
	assert.Nil(t, shortCircuit,
		"explicitly free work costs nothing: the funnel serves it without a hold, exactly as free chat work")
	assert.Equal(t, 0, fake.calls())
}
