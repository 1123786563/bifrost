// The streaming final-usage settlement (07.10): a stream settles at its FINAL usage -
// the last chunk's real report, or, when the client hangs up first, the usage the provider
// already processed riding the cancellation's BilledUsage. A disconnect is never treated
// as a no-charge: what was consumed is settled, and what cannot be confirmed is recorded
// UNKNOWN for recovery (08.x) with the hold kept occupied - never settled as a zero,
// never dropped. Stream and non-stream travel the same settle contract under the same
// idempotency key rule: the attempt identity.
//
// The tests drive the seam the pipeline drives: PreLLMHook holds the reservation, the
// provider streams chunks through PostLLMHook (one call per chunk), and the endings the
// pipeline hands the post hook - the final chunk, the 499 cancellation with its billed
// usage, the final chunk without a usable report - are exactly what these tests hand it.
package governance

import (
	"net/http"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/maximhq/bifrost/core/schemas"
)

// streamChatChunk is one streamed chat chunk the way the pipeline carries it: a response
// whose extra fields name the stream request type, so the post hook reads it as streaming.
func streamChatChunk(usage *schemas.BifrostLLMUsage) *schemas.BifrostResponse {
	response := &schemas.BifrostResponse{
		ChatResponse: &schemas.BifrostChatResponse{Usage: usage},
	}
	response.PopulateExtraFields(schemas.ChatCompletionStreamRequest, schemas.OpenAI, reservePaidModel, reservePaidModel)
	return response
}

// streamClientDisconnectError is the 499 the pipeline builds when the client hangs up
// mid-stream: the stream end indicator is set (the ending is terminal), and the provider's
// already-processed usage - whatever the accumulator held - rides the error as BilledUsage.
func streamClientDisconnectError(billedUsage *schemas.BifrostLLMUsage) *schemas.BifrostError {
	cancelErr := &schemas.BifrostError{
		StatusCode: schemas.Ptr(499),
		Error: &schemas.ErrorField{
			Message: "Request cancelled: client disconnected",
			Type:    schemas.Ptr(schemas.RequestCancelled),
		},
	}
	if billedUsage != nil {
		cancelErr.ExtraFields.BilledUsage = billedUsage
	}
	// Core stamps these on every error before the post hooks read it.
	cancelErr.PopulateExtraFields(schemas.ChatCompletionStreamRequest, schemas.OpenAI, reservePaidModel, reservePaidModel)
	return cancelErr
}

// TestStreamSettlementFinalChunkSettlesTheFinalRealUsageNotThePartial holds AC07's first
// clause: the full streamed response settles by its final real usage - the mid-stream
// partial settles nothing, and the one settle message carries the final chunk's amounts,
// the same numbers the ledger will hold.
func TestStreamSettlementFinalChunkSettlesTheFinalRealUsageNotThePartial(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := reserveTestCtx()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "the reserve held; the attempt dispatches")

	// The stream's mid-flight chunks carry running partials: none of them settles.
	_, _, err = plugin.PostLLMHook(ctx, streamChatChunk(&schemas.BifrostLLMUsage{
		PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15,
	}), nil)
	require.NoError(t, err)
	plugin.wg.Wait()
	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "a mid-stream partial settles nothing")
	assert.Equal(t, 0, door.callCount())

	// The final chunk carries the stream's real usage: one settlement, on those amounts.
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	_, _, err = plugin.PostLLMHook(ctx, streamChatChunk(&schemas.BifrostLLMUsage{
		PromptTokens:     600,
		CompletionTokens: 400,
		TotalTokens:      1000,
		PromptTokensDetails: &schemas.ChatPromptTokensDetails{
			CachedReadTokens:  100,
			CachedWriteTokens: 50,
		},
	}), nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	assert.Equal(t, 1, door.callCount(), "the stream settles exactly once, on its final usage")
	_, body := door.snapshot()
	require.NotNil(t, body)
	assert.Equal(t, map[string]any{
		"input": float64(450), "output": float64(400), "reasoning": float64(0),
		"image": float64(0), "image_output": float64(0),
		"audio_input": float64(0), "audio_output": float64(0),
		"cache_read": float64(100), "cache_write": float64(50),
		"video": float64(0), "surcharge_web_search": float64(0), "surcharge_per_request": float64(0),
	}, body["actualUsage"], "the settled amount is the final real usage, not a mid-stream partial")

	pending, err = plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	assert.Empty(t, pending, "the delivered settlement leaves the retry queue")
}

// TestStreamSettlementClientDisconnectStillSettlesTheUsageAlreadyProcessed holds AC07's
// second clause: a client that hangs up mid-stream does not take the bill with it. The
// usage the provider already processed rides the cancellation's BilledUsage, and it is
// settled through the same door under the attempt's idempotency key - never treated as a
// no-charge.
func TestStreamSettlementClientDisconnectStillSettlesTheUsageAlreadyProcessed(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := reserveTestCtx()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "the reserve held; the attempt dispatches")

	// Tokens streamed and consumed before the hang-up: real provider work, whatever
	// happens to the client's connection next.
	_, _, err = plugin.PostLLMHook(ctx, streamChatChunk(&schemas.BifrostLLMUsage{
		PromptTokens: 600, CompletionTokens: 120, TotalTokens: 720,
	}), nil)
	require.NoError(t, err)

	// The client disconnects: the pipeline ends the stream with the 499 cancellation,
	// the accumulator's usage attached as the error's BilledUsage (600 prompt tokens
	// held for the whole call, 180 completion tokens actually processed by then).
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	cancelErr := streamClientDisconnectError(&schemas.BifrostLLMUsage{
		PromptTokens: 600, CompletionTokens: 180, TotalTokens: 780,
	})
	_, _, err = plugin.PostLLMHook(ctx, nil, cancelErr)
	require.NoError(t, err)
	plugin.wg.Wait()

	assert.Equal(t, 1, door.callCount(), "the occurred usage is settled despite the disconnect")
	_, body := door.snapshot()
	require.NotNil(t, body)
	assert.Equal(t, map[string]any{
		"input": float64(600), "output": float64(180), "reasoning": float64(0),
		"image": float64(0), "image_output": float64(0),
		"audio_input": float64(0), "audio_output": float64(0),
		"cache_read": float64(0), "cache_write": float64(0),
		"video": float64(0), "surcharge_web_search": float64(0), "surcharge_per_request": float64(0),
	}, body["actualUsage"], "the settled amount is the usage the provider already processed")
	assert.Equal(t, map[string]any{"kind": "provider_usage_report", "reference": "req-reserve-1"}, body["evidence"],
		"the settled usage still stands on the provider's own report")

	reservation, ok := ctx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok)
	assert.Equal(t, reservation.AttemptID, body["idempotencyKey"],
		"the disconnect's settlement retries under the same attempt identity")

	all, ok := plugin.settlementOutbox.(*localSettlementOutbox)
	require.True(t, ok)
	records, err := all.snapshot(ctx)
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, SettlementOutcomeProviderUsageReport, records[0].Outcome)
	assert.Equal(t, SettlementStatusDelivered, records[0].Status, "the occurred usage is settled, not held")
}

// TestStreamSettlementDisconnectWithoutMeasurableUsageIsRecordedUnknownAndHolds holds
// AC07's third clause: a disconnect whose consumed usage cannot be confirmed is not free
// and not invented - it is recorded UNKNOWN, the settle door hears nothing, and the hold
// stays occupied until evidence (08.x) says settle or release.
func TestStreamSettlementDisconnectWithoutMeasurableUsageIsRecordedUnknownAndHolds(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := reserveTestCtx()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "the reserve held; the attempt dispatches")

	// A disconnect before anything measurable: no accumulator, no BilledUsage.
	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	cancelErr := streamClientDisconnectError(nil)
	_, _, err = plugin.PostLLMHook(ctx, nil, cancelErr)
	require.NoError(t, err)
	plugin.wg.Wait()

	assert.Equal(t, 0, door.callCount(), "an unconfirmed usage is never settled as a zero")
	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "the unconfirmable ending is persisted for recovery, not dropped")
	assert.Equal(t, SettlementOutcomeUsageUnknown, pending[0].Outcome)
	assert.Equal(t, SettlementStatusPending, pending[0].Status,
		"the hold stays occupied until evidence says settle or release")
}

// TestStreamSettlementFinalChunkWithoutAUsableReportIsRecordedUnknownAndHolds holds AC07's
// third clause for the clean ending: a stream that terminates without the provider ever
// reporting usable usage is recorded UNKNOWN - the hold stays, recovery asks the question.
func TestStreamSettlementFinalChunkWithoutAUsableReportIsRecordedUnknownAndHolds(t *testing.T) {
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	door := newFakeSettleDoor(t, http.StatusOK)
	plugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), door.endpoint(), 0))

	ctx := reserveTestCtx()
	_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit, "the reserve held; the attempt dispatches")

	ctx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	_, _, err = plugin.PostLLMHook(ctx, streamChatChunk(nil), nil)
	require.NoError(t, err)
	plugin.wg.Wait()

	assert.Equal(t, 0, door.callCount(), "no report, no settle: the usage is unknown, not zero")
	pending, err := plugin.settlementOutbox.Pending(ctx)
	require.NoError(t, err)
	require.Len(t, pending, 1, "the stream ended without usage: the record holds for recovery")
	assert.Equal(t, SettlementOutcomeUsageUnknown, pending[0].Outcome)
	assert.Equal(t, SettlementStatusPending, pending[0].Status)
}

// TestStreamSettlementRidesTheSameSettleContractAsTheNonStreamPath holds AC07's fourth
// clause: the stream's settlement and the non-stream's settlement are the same message -
// the same fields, the same evidence kind, the same settled-by, and the same idempotency
// key rule - so OpenBot's Settle fence cannot tell the paths apart and needs no second
// contract for streams.
func TestStreamSettlementRidesTheSameSettleContractAsTheNonStreamPath(t *testing.T) {
	// The non-stream path: one attempt, settled from its response's usage.
	reserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	nonStreamDoor := newFakeSettleDoor(t, http.StatusOK)
	nonStream := newReserveTestPlugin(t, admissionWithReserveAndSettle(reserve.endpoint(), nonStreamDoor.endpoint(), 0))
	nonStreamCtx := settleAttemptFor(t, nonStream, settledChatResponse(), nil)
	nonStream.wg.Wait()
	require.Equal(t, 1, nonStreamDoor.callCount())
	_, nonStreamBody := nonStreamDoor.snapshot()
	require.NotNil(t, nonStreamBody)

	// The stream path: one attempt, ended by a client disconnect, settled from the
	// usage the cancellation carried.
	streamReserve := newFakeCreditService(t, http.StatusCreated, reservedAnswer())
	streamDoor := newFakeSettleDoor(t, http.StatusOK)
	streamPlugin := newReserveTestPlugin(t, admissionWithReserveAndSettle(streamReserve.endpoint(), streamDoor.endpoint(), 0))
	streamCtx := reserveTestCtx()
	_, shortCircuit, err := streamPlugin.PreLLMHook(streamCtx, reserveChatRequest(reservePaidModel))
	require.NoError(t, err)
	require.Nil(t, shortCircuit)
	streamCtx.SetValue(schemas.BifrostContextKeyStreamEndIndicator, true)
	_, _, err = streamPlugin.PostLLMHook(streamCtx, nil, streamClientDisconnectError(&schemas.BifrostLLMUsage{
		PromptTokens: 600, CompletionTokens: 180, TotalTokens: 780,
	}))
	require.NoError(t, err)
	streamPlugin.wg.Wait()
	require.Equal(t, 1, streamDoor.callCount())
	_, streamBody := streamDoor.snapshot()
	require.NotNil(t, streamBody)

	nonStreamKeys := make([]string, 0, len(nonStreamBody))
	for key := range nonStreamBody {
		nonStreamKeys = append(nonStreamKeys, key)
	}
	streamKeys := make([]string, 0, len(streamBody))
	for key := range streamBody {
		streamKeys = append(streamKeys, key)
	}
	sort.Strings(nonStreamKeys)
	sort.Strings(streamKeys)
	assert.Equal(t, nonStreamKeys, streamKeys,
		"one contract: the stream's settle message names exactly what the non-stream's names")
	assert.Equal(t, nonStreamBody["settledBy"], streamBody["settledBy"])
	assert.Equal(t, nonStreamBody["evidence"], map[string]any{"kind": "provider_usage_report", "reference": "req-reserve-1"})

	nonStreamReservation, ok := nonStreamCtx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok)
	streamReservation, ok := streamCtx.Value(CreditReservationContextKey).(*CreditReservation)
	require.True(t, ok)
	assert.Equal(t, nonStreamReservation.AttemptID, nonStreamBody["idempotencyKey"])
	assert.Equal(t, streamReservation.AttemptID, streamBody["idempotencyKey"],
		"one idempotency rule: the attempt identity, on both paths")
}
