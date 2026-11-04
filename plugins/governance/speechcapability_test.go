package governance

// The speech tail-constraint gate (Bifrost 13.4, #309), tested the way the audio
// capability gate is tested: through PreLLMHook, against the fake reserve door, so the
// provider-side proof (供应商调用数为 0) is structural — the refusal is a pre-hook short
// circuit the reserve door was never part of.
//
// WHAT THE ACs DEMAND OF THESE TESTS:
//   无法约束输出时长或尾部费用的语音路径不可用 — every response format except wav (and
//     the wire's own mp3 default for an absent format) refuses on BOTH speech entrances,
//     with the format and both reasons named in the refusal;
//   强制调用不可用路径时被短路拒绝，供应商调用数为 0 — the reserve door records zero
//     calls, the request is marked refused for the post hooks' accounting;
//   可约束路径正常执行 — a wav request with VERIFIED audio capability records passes the
//     tail gate (whatever the pricing axis then says is its own question), and non-speech
//     request types are untouched by the tail table in every configuration.

import (
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// speechRequestWithFormat is a synthesis request for the model named, carrying the wire's
// own response_format field — the shape the tail gate reads.
func speechRequestWithFormat(model, format string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.SpeechRequest,
		SpeechRequest: &schemas.BifrostSpeechRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Params: &schemas.SpeechParameters{
				ResponseFormat: format,
			},
		},
	}
}

// speechStreamRequestWithFormat is the synthesis entrance's stream variant with a format.
func speechStreamRequestWithFormat(model, format string) *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.SpeechStreamRequest,
		SpeechRequest: &schemas.BifrostSpeechRequest{
			Provider: schemas.OpenAI,
			Model:    model,
			Params: &schemas.SpeechParameters{
				ResponseFormat: format,
			},
		},
	}
}

func TestSpeechCapabilityTailTable(t *testing.T) {
	plugin := newReserveTestPlugin(t, admissionWithReserve("", 0))

	t.Run("wav is the only constrainable format on both entrances", func(t *testing.T) {
		for _, format := range []string{"wav"} {
			request := &EvaluationRequest{
				RequestType: schemas.SpeechRequest,
				LLMRequest:  speechRequestWithFormat(reserveFreeModel, format),
			}
			assert.Equal(t, "", plugin.speechTailProblem(request),
				"wav carries the container meter; the tail gate does not refuse it")
			stream := &EvaluationRequest{
				RequestType: schemas.SpeechStreamRequest,
				LLMRequest:  speechStreamRequestWithFormat(reserveFreeModel, format),
			}
			assert.Equal(t, "", plugin.speechTailProblem(stream))
		}
	})

	t.Run("every other wire format refuses with both reasons, naming the format", func(t *testing.T) {
		for _, format := range []string{"mp3", "opus", "aac", "flac", "pcm"} {
			request := &EvaluationRequest{
				RequestType: schemas.SpeechRequest,
				LLMRequest:  speechRequestWithFormat(reserveFreeModel, format),
			}
			problem := plugin.speechTailProblem(request)
			require.NotEmpty(t, problem, "%s has no server-readable meter", format)
			assert.Contains(t, problem, "/v1/audio/speech")
			assert.Contains(t, problem, format)
			assert.Contains(t, problem, "no server-readable duration meter exists",
				"the generated amount is undeterminable")
			assert.Contains(t, problem, "cannot stop the billing",
				"the tail cost is unenforceable")
			assert.Contains(t, problem, "no provider is called")
		}
	})

	t.Run("an absent format is the wire's mp3 default, and it refuses as mp3", func(t *testing.T) {
		request := &EvaluationRequest{
			RequestType: schemas.SpeechRequest,
			// The typed body carries no Params at all — the wire default substitutes.
			LLMRequest: &schemas.BifrostRequest{
				RequestType:   schemas.SpeechRequest,
				SpeechRequest: &schemas.BifrostSpeechRequest{Provider: schemas.OpenAI, Model: reserveFreeModel},
			},
		}
		problem := plugin.speechTailProblem(request)
		require.NotEmpty(t, problem)
		assert.Contains(t, problem, `"mp3"`)
		assert.Contains(t, problem, "the wire default for an absent format",
			"the refusal says the default substitution, never a silent pass")
	})

	t.Run("a request whose typed body the funnel does not carry refuses as the default", func(t *testing.T) {
		request := &EvaluationRequest{RequestType: schemas.SpeechRequest}
		assert.NotEmpty(t, plugin.speechTailProblem(request),
			"a call whose format the gateway cannot read is a call whose tail it cannot bound")
	})

	t.Run("a format outside the wire's closed set refuses with itself named", func(t *testing.T) {
		request := &EvaluationRequest{
			RequestType: schemas.SpeechRequest,
			LLMRequest:  speechRequestWithFormat(reserveFreeModel, "ogg"),
		}
		problem := plugin.speechTailProblem(request)
		require.NotEmpty(t, problem)
		assert.Contains(t, problem, "ogg")
		assert.Contains(t, problem, "not even in the wire's closed format set")
	})

	t.Run("non-speech request types are not this gate's question", func(t *testing.T) {
		chat := &EvaluationRequest{RequestType: schemas.ChatCompletionRequest}
		assert.Equal(t, "", plugin.speechTailProblem(chat))
		transcription := &EvaluationRequest{RequestType: schemas.TranscriptionRequest}
		assert.Equal(t, "", plugin.speechTailProblem(transcription),
			"transcription bills its input audio — the tail table is synthesis-only")
	})
}

func TestSpeechCapabilityGateShortCircuitsBeforeTheReserveDoor(t *testing.T) {
	fake := newFakeCreditService(t, 201, reservedAnswer())

	t.Run("a forced call on an unconstrainable format — even with verified audio records", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionArmedWithAudioEvidence(
			admissionWithReserve(fake.endpoint(), 0),
			[]AudioCapabilityRecord{
				audioVerifiedRecord("/v1/audio/speech", false),
				audioVerifiedRecord("/v1/audio/speech", true),
			}))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, speechRequestWithFormat(reserveFreeModel, "mp3"))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit,
			"an unconstrainable tail is not callable — even for a free model with verified path records")
		require.NotNil(t, shortCircuit.Error.Error)
		require.NotNil(t, shortCircuit.Error.Error.Code)
		assert.Equal(t, CreditRefusalCapNotProvable, *shortCircuit.Error.Error.Code,
			"a tail nobody can bound is a cap nobody can prove")
		assert.Contains(t, shortCircuit.Error.Error.Message, "speech_tail")
		assert.Contains(t, shortCircuit.Error.Error.Message, "mp3")
		assert.Equal(t, 0, fake.calls(),
			"the reserve door is never asked for a call mode whose tail cannot be bounded")
		assert.Equal(t, true, ctx.Value(governanceRejectedContextKey),
			"a refused request is marked refused so the post hooks write no accounting")
	})

	t.Run("the absent-format default refuses identically", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionArmedWithAudioEvidence(
			admissionWithReserve(fake.endpoint(), 0),
			[]AudioCapabilityRecord{audioVerifiedRecord("/v1/audio/speech", false)}))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, &schemas.BifrostRequest{
			RequestType:   schemas.SpeechRequest,
			SpeechRequest: &schemas.BifrostSpeechRequest{Provider: schemas.OpenAI, Model: reserveFreeModel},
		})
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		require.NotNil(t, shortCircuit.Error.Error)
		assert.Contains(t, shortCircuit.Error.Error.Message, `"mp3"`)
		assert.Equal(t, 0, fake.calls())
	})

	t.Run("the stream entrance's unconstrainable formats refuse too", func(t *testing.T) {
		plugin := newReserveTestPlugin(t, admissionArmedWithAudioEvidence(
			admissionWithReserve(fake.endpoint(), 0),
			[]AudioCapabilityRecord{audioVerifiedRecord("/v1/audio/speech", true)}))
		ctx := reserveTestCtx()

		_, shortCircuit, err := plugin.PreLLMHook(ctx, speechStreamRequestWithFormat(reserveFreeModel, "opus"))
		require.NoError(t, err)
		require.NotNil(t, shortCircuit)
		require.NotNil(t, shortCircuit.Error.Error)
		assert.Contains(t, shortCircuit.Error.Error.Message, "stream=true")
		assert.Contains(t, shortCircuit.Error.Error.Message, "opus")
		assert.Equal(t, 0, fake.calls())
	})
}

func TestSpeechCapabilityConstrainablePathProceeds(t *testing.T) {
	// The constrainable format passes the tail gate and the verified audio gate, and then
	// meets the pricing classification like any other request: with the free model the
	// reserve fixture names, the request proceeds (no short circuit from these gates), so
	// the tail table disables only what it must — 宁可少开放一个路径，也不放错一个.
	fake := newFakeCreditService(t, 201, reservedAnswer())
	plugin := newReserveTestPlugin(t, admissionArmedWithAudioEvidence(
		admissionWithReserve(fake.endpoint(), 0),
		[]AudioCapabilityRecord{audioVerifiedRecord("/v1/audio/speech", false)}))
	ctx := reserveTestCtx()

	_, shortCircuit, err := plugin.PreLLMHook(ctx, speechRequestWithFormat(reserveFreeModel, "wav"))
	require.NoError(t, err)
	assert.Nil(t, shortCircuit,
		"a wav call on a verified entrance is not refused by the tail gate or the audio gate")
}
