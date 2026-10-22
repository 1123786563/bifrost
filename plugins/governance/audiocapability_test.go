package governance

// The audio path capability gate (Bifrost 14.4, #313), tested the way the output-cap
// evidence gate is tested: through PreLLMHook, against the fake reserve door, so the
// provider-side proof (调用数为 0) is structural — the refusal is a pre-hook short circuit
// the reserve door was never part of.
//
// WHAT THE ACs DEMAND OF THESE TESTS:
//   未验证音频路径不可调用，供应商调用数为 0 — an audio entrance with no VERIFIED record
//     short-circuits before the pricing classification, the reserve door records zero
//     calls, and the request is marked refused for the post hooks' accounting;
//   拒绝原因指向缺失的具体证据项 — the refusal message names the path, its stream variant
//     and every missing-item code the record carries;
//   补齐证据后路径可启用，无需改动其他能力 — a VERIFIED record for the entrance lets the
//     same request through the gate (a free model then proceeds, no short circuit), while
//     chat requests are untouched by audio records in every configuration.

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// speechRequest is a speech (synthesis) request for the model named, the funnel's own
// shape. 13.4 (#309) added the tail-constraint gate ahead of this one, so the fixture
// carries the CONSTRAINABLE response format (wav): this file's subject is the per-entrance
// capability gate, and a wav request is the one the tail gate lets reach it.
func speechRequest(model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.SpeechRequest,
		SpeechRequest: &schemas.BifrostSpeechRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Params: &schemas.SpeechParameters{
				ResponseFormat: "wav",
			},
		},
	}
}

// transcriptionStreamRequest is the recognition entrance's stream variant — the identity
// the registry keys on, exercised through the gate.
func transcriptionStreamRequest(model string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.TranscriptionStreamRequest,
		TranscriptionRequest: &schemas.BifrostTranscriptionRequest{
			Provider: schemas.OpenAI,
			Model:    model,
		},
	}
}

// audioVerifiedRecord is a complete, valid, verified capability record for one entrance.
func audioVerifiedRecord(path string, stream bool) AudioCapabilityRecord {
	return AudioCapabilityRecord{
		Path:        path,
		Stream:      stream,
		Environment: "fake-provider-harness",
		Result:      AudioCapabilityEvidenceVerified,
		VerifiedAt:  "2026-10-21T00:00:00Z",
		Source:      "go test ./plugins/governance -run AudioCapability",
	}
}

// audioUnverifiedRecord carries the registry's own missing items — the shape OpenBot's
// 14.3 gap report writes for the passthrough-only translations path, transplanted onto a
// speech record for the refusal-message assertions.
func audioUnverifiedRecord(path string, stream bool) AudioCapabilityRecord {
	record := audioVerifiedRecord(path, stream)
	record.Result = AudioCapabilityEvidenceUnverified
	record.MissingItems = []AudioCapabilityMissingItem{
		{Code: AudioCapabilityMissingNoInputMeterWired, Detail: "no server-trusted measurement exists for this route"},
		{Code: AudioCapabilityMissingNoAdmissionWiring, Detail: "no Reserve/Settle wiring exists to admit it"},
	}
	return record
}

// admissionArmedWithAudioEvidence copies a base admission config and sets exactly the audio
// capability records the test asks for.
func admissionArmedWithAudioEvidence(base *CreditAdmissionConfig, records []AudioCapabilityRecord) *CreditAdmissionConfig {
	base.AudioCapabilityEvidence = records
	return base
}

func TestAudioCapabilityGateRefusesUnverifiedPaths(t *testing.T) {
	fake := newFakeCreditService(t, 201, reservedAnswer())

	t.Run("no audio records at all", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, speechRequest(reserveFreeModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit,
			"an audio entrance no record verifies is not callable — even for a free model")
		require.NotNil(t, shortCircuit.Error.Error)
		require.NotNil(t, shortCircuit.Error.Error.Code)
		assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code,
			"an unverified audio path is a cap_not_provable refusal: the ceiling cannot be proven")
		assert.Contains(t, shortCircuit.Error.Error.Message, "/v1/audio/speech",
			"the refusal names the path it refuses")
		assert.Contains(t, shortCircuit.Error.Error.Message, "no capability evidence record",
			"the refusal says what is missing")
		assert.Equal(t, 0, fake.calls(),
			"the reserve door is never asked for a path no evidence verifies")
		assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
			"a refused request is marked refused so the post hooks write no accounting")
	})

	t.Run("an unverified record keeps the path refused, naming the missing items", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionArmedWithAudioEvidence(
			admissionWithReserve(fake.endpoint(), 0),
			[]AudioCapabilityRecord{audioUnverifiedRecord("/v1/audio/speech", false)}))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, speechRequest(reserveFreeModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		require.NotNil(t, shortCircuit.Error.Error)
		assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code)
		assert.Contains(t, shortCircuit.Error.Error.Message, AudioCapabilityEvidenceUnverified,
			"the refusal carries the recorded result")
		assert.Contains(t, shortCircuit.Error.Error.Message, AudioCapabilityMissingNoInputMeterWired,
			"the refusal points at the specific missing evidence item")
		assert.Contains(t, shortCircuit.Error.Error.Message, AudioCapabilityMissingNoAdmissionWiring,
			"every missing item is named")
		assert.Contains(t, shortCircuit.Error.Error.Message, "no server-trusted measurement exists for this route",
			"the item's own detail travels with the code")
		assert.Equal(t, 0, fake.calls())
	})

	t.Run("a not_enforced record keeps the path refused", func(t *testing.T) {
		record := audioVerifiedRecord("/v1/audio/transcriptions", true)
		record.Result = AudioCapabilityEvidenceNotEnforced
		record.MissingItems = []AudioCapabilityMissingItem{
			{Code: AudioCapabilityMissingNoWirePriceSource, Detail: "no curated basis prices the cell"},
		}
		plugin := newReserveTestPlugin(t, admissionArmedWithAudioEvidence(
			admissionWithReserve(fake.endpoint(), 0), []AudioCapabilityRecord{record}))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, transcriptionStreamRequest(reserveFreeModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		require.NotNil(t, shortCircuit.Error.Error)
		assert.Contains(t, shortCircuit.Error.Error.Message, "/v1/audio/transcriptions")
		assert.Contains(t, shortCircuit.Error.Error.Message, AudioCapabilityEvidenceNotEnforced)
		assert.Equal(t, 0, fake.calls())
	})

	t.Run("the stream variant is its own entrance: a unary record verifies nothing for it", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionArmedWithAudioEvidence(
			admissionWithReserve(fake.endpoint(), 0),
			[]AudioCapabilityRecord{audioVerifiedRecord("/v1/audio/speech", false)}))
		ctx := reserveTestCtx()

		request := &schemas.BifrostRequest{
			RequestType: schemas.SpeechStreamRequest,
			SpeechRequest: &schemas.BifrostSpeechRequest{
				Provider: schemas.OpenAI,
				Model:    reserveFreeModel,
			},
		}
		_, shortCircuit, err := plugin.PreLLMHook(ctx, request)
		require.NoError(t, err)
		require.NotNil(t, shortCircuit, "the stream entrance is a different cell: its own record must verify it")
		assert.Contains(t, shortCircuit.Error.Error.Message, "stream=true")
		assert.Equal(t, 0, fake.calls())
	})

	t.Run("a malformed record refuses its path with the problem visible", func(t *testing.T) {
		bad := audioVerifiedRecord("/v1/audio/speech", false)
		bad.Result = "probably-fine"
		plugin := newReserveTestPlugin(t, admissionArmedWithAudioEvidence(
			admissionWithReserve(fake.endpoint(), 0), []AudioCapabilityRecord{bad}))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, speechRequest(reserveFreeModel))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		assert.Contains(t, shortCircuit.Error.Error.Message, "unknown result",
			"malformed evidence is as visible as absent evidence")
		assert.Equal(t, 0, fake.calls())
	})
}

func TestAudioCapabilityVerifiedPathIsCallableWithoutTouchingOtherCapabilities(t *testing.T) {
	fake := newFakeCreditService(t, 201, reservedAnswer())

	t.Run("a verified record lets the same request through the gate", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionArmedWithAudioEvidence(
			admissionWithReserve(fake.endpoint(), 0),
			[]AudioCapabilityRecord{audioVerifiedRecord("/v1/audio/speech", false)}))

		ctx := reserveTestCtx()
		_, shortCircuit, err := plugin.PreLLMHook(ctx, speechRequest(reserveFreeModel))
		require.NoError(t, err)
		// 补齐证据后路径可启用: the free model proceeds past the audio gate and the
		// pricing classification — no short circuit, no door call (free reserves nothing).
		require.Nil(t, shortCircuit,
			"a verified audio entrance over a free model is not the gate's question anymore")
		assert.Equal(t, 0, fake.calls(), "free work reserves nothing")
	})

	t.Run("chat requests are untouched by audio records in every configuration", func(t *testing.T) {
		// No audio records at all: a chat request must behave exactly as it did before
		// this gate existed — the gate answers "" for non-audio request types without
		// reading the configured records.
		plugin := newReserveTestPlugin(t, admissionWithReserve(fake.endpoint(), 0))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, reserveChatRequest(reserveFreeModel))
		require.NoError(t, err)
		require.Nil(t, shortCircuit, "a free chat model proceeds as before the audio gate")
	})
}

func TestAudioCapabilityRecordStructureIsClosedAndComplete(t *testing.T) {
	complete := audioVerifiedRecord("/v1/audio/speech", false)
	assert.Equal(t, "", complete.evidenceProblem(), "a complete verified record has no problem")

	unverified := audioUnverifiedRecord("/v1/audio/speech", false)
	assert.Equal(t, "", unverified.evidenceProblem(),
		"an unverified record WITH its missing items named is complete evidence of its own refusal")

	cases := map[string]struct {
		mutate  func(*AudioCapabilityRecord)
		problem string
	}{
		"no path":        {func(r *AudioCapabilityRecord) { r.Path = "" }, "names no path"},
		"no environment": {func(r *AudioCapabilityRecord) { r.Environment = "" }, "names no verification environment"},
		"unknown result": {func(r *AudioCapabilityRecord) { r.Result = "likely" }, "unknown result"},
		"unverified without items": {func(r *AudioCapabilityRecord) {
			r.Result = AudioCapabilityEvidenceUnverified
		}, "carries no missing items"},
		"no verified-at": {func(r *AudioCapabilityRecord) { r.VerifiedAt = "" }, "names no verification time"},
		"no source":      {func(r *AudioCapabilityRecord) { r.Source = "" }, "names no reproducing source"},
		"unknown item code": {func(r *AudioCapabilityRecord) {
			r.Result = AudioCapabilityEvidenceUnverified
			r.MissingItems = []AudioCapabilityMissingItem{{Code: "vibes", Detail: "felt good"}}
		}, "unknown code"},
		"item without detail": {func(r *AudioCapabilityRecord) {
			r.Result = AudioCapabilityEvidenceUnverified
			r.MissingItems = []AudioCapabilityMissingItem{{Code: AudioCapabilityMissingNoInputMeterWired}}
		}, "carries no detail"},
	}
	for name, tc := range cases {
		record := audioVerifiedRecord("/v1/audio/speech", false)
		tc.mutate(&record)
		assert.Contains(t, record.evidenceProblem(), tc.problem, name)
	}
}

func TestAudioRequestEntranceMapping(t *testing.T) {
	cases := []struct {
		requestType schemas.RequestType
		path        string
		stream      bool
		isAudio     bool
	}{
		{schemas.SpeechRequest, "/v1/audio/speech", false, true},
		{schemas.SpeechStreamRequest, "/v1/audio/speech", true, true},
		{schemas.TranscriptionRequest, "/v1/audio/transcriptions", false, true},
		{schemas.TranscriptionStreamRequest, "/v1/audio/transcriptions", true, true},
		{schemas.ChatCompletionRequest, "", false, false},
		{schemas.EmbeddingRequest, "", false, false},
		{schemas.OCRRequest, "", false, false},
		{schemas.PassthroughRequest, "", false, false},
	}
	for _, tc := range cases {
		path, stream, isAudio := audioRequestEntrance(tc.requestType)
		assert.Equal(t, tc.path, path, string(tc.requestType))
		assert.Equal(t, tc.stream, stream, string(tc.requestType))
		assert.Equal(t, tc.isAudio, isAudio, string(tc.requestType))
	}
}
