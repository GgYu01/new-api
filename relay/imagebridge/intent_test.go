package imagebridge

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDetectJSONResponsesImageToolWithoutModelUsesC2ADefault(t *testing.T) {
	body := []byte(`{
		"input":[{"role":"user","content":[{"type":"input_text","text":"draw a red fox"}]}],
		"tools":[{"type":"image_generation","quality":"low"}],
		"stream":true
	}`)

	intent, ok, err := DetectJSON("/v1/responses", body)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, EnvelopeResponses, intent.Envelope)
	assert.Equal(t, ModeGeneration, intent.Mode)
	assert.Equal(t, "", intent.ClientModel)
	assert.Equal(t, DefaultModel, intent.Request.Model)
	assert.Equal(t, "draw a red fox", intent.Request.Prompt)
	assert.Equal(t, "high", intent.Request.Quality)
	assert.True(t, intent.Stream)
}

func TestDetectJSONChatFunctionToolKeepsClientModelOnlyForResponseEnvelope(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"messages":[{"role":"user","content":"make a 16:9 blue poster"}],
		"tools":[{"type":"function","function":{"name":"image_generation"}}]
	}`)

	intent, ok, err := DetectJSON("/v1/chat/completions", body)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, EnvelopeChat, intent.Envelope)
	assert.Equal(t, "gpt-5.6-sol", intent.ClientModel)
	assert.Equal(t, DefaultModel, intent.Request.Model)
	assert.Equal(t, "1536x1024", intent.Request.Size)
}

func TestDetectJSONExplicitForeignImageModelRemainsEligibleForPublicImageRoute(t *testing.T) {
	body := []byte(`{"model":"grok-imagine-image","prompt":"a lighthouse"}`)

	intent, ok, err := DetectJSON("/v1/images/generations", body)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "grok-imagine-image", intent.ClientModel)
	assert.Equal(t, "grok-imagine-image", intent.Request.Model)
}

func TestDetectJSONExplicitForeignImageModelKeepsChannelSelectionModel(t *testing.T) {
	for _, model := range []string{
		"grok-imagine-image",
		"grok-imagine-image-quality",
		"grok-imagine-image-2.0",
	} {
		t.Run(model, func(t *testing.T) {
			body := []byte(`{"model":"` + model + `","prompt":"a lighthouse"}`)

			intent, ok, err := DetectJSON("/v1/images/generations", body)

			require.NoError(t, err)
			require.True(t, ok)
			assert.Equal(t, model, intent.ClientModel)
			assert.Equal(t, model, intent.Request.Model)
		})
	}
}

func TestDetectJSONForeignGrokImageToolKeepsChannelSelectionModel(t *testing.T) {
	body := []byte(`{"model":"grok-4.6","input":"draw a lighthouse","tools":[{"type":"image_generation"}]}`)

	intent, ok, err := DetectJSON("/v1/responses", body)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "grok-4.6", intent.ClientModel)
	assert.Equal(t, "grok-4.6", intent.Request.Model)
}

func TestKnownGrokModelsAreNeverClassifiedAsOpenAIFamily(t *testing.T) {
	for _, model := range []string{
		"grok-3-mini", "grok-3-mini-fast", "grok-4.20-0309-non-reasoning",
		"grok-4.20-0309-reasoning", "grok-4.20-multi-agent-0309", "grok-4.3",
		"grok-4.5", "grok-4.6", "grok-build-0.1", "grok-code-fast",
		"grok-code-fast-1", "grok-code-fast-1-0825", "grok-composer-2.5-fast",
		"grok-imagine-image", "grok-imagine-image-2.0", "grok-imagine-image-quality",
		"grok-imagine-video", "grok-imagine-video-1.5", "grok-imagine-video-1.5-preview",
	} {
		assert.Falsef(t, isOpenAIFamilyModel(model), "model %q must remain outside the OpenAI/C2A family", model)
	}
}

func TestDetectJSONOrdinaryTextDoesNotBecomeAnImageRequest(t *testing.T) {
	body := []byte(`{"model":"gpt-5.6-sol","input":"draw a conclusion from this report"}`)

	_, ok, err := DetectJSON("/v1/responses", body)

	require.NoError(t, err)
	assert.False(t, ok)
}

func TestDetectJSONImagesGenerationWithoutModelIsAccepted(t *testing.T) {
	intent, ok, err := DetectJSON("/v1/images/generations", []byte(`{"prompt":"a tree"}`))

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, EnvelopeImages, intent.Envelope)
	assert.Equal(t, DefaultModel, intent.Request.Model)
	assert.Equal(t, "a tree", intent.Request.Prompt)
}

func TestDetectJSONVariationBecomesEditWithDefaultPrompt(t *testing.T) {
	body := []byte(`{"image":"data:image/png;base64,iVBORw0KGgo="}`)

	intent, ok, err := DetectJSON("/v1/images/variations", body)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ModeEdit, intent.Mode)
	assert.Equal(t, DefaultVariationPrompt, intent.Request.Prompt)
	assert.Equal(t, 1, intent.ImageCount)
}

func TestDetectJSONGenerationWithInputImageBecomesEdit(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"role":"user","content":[
			{"type":"input_text","text":"turn this into a watercolor"},
			{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}
		]}],
		"tools":[{"type":"image_generation"}]
	}`)

	intent, ok, err := DetectJSON("/v1/responses", body)

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, ModeEdit, intent.Mode)
	assert.Equal(t, 1, intent.ImageCount)
	assert.Equal(t, "turn this into a watercolor", intent.Request.Prompt)
}

func TestDetectJSONClampsCountAndFloorsQuality(t *testing.T) {
	body := []byte(`{"model":"dall-e-3","prompt":"square icon","n":999,"quality":"standard","size":"1:1"}`)

	intent, ok, err := DetectJSON("/v1/images/generations", body)

	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, intent.Request.N)
	assert.EqualValues(t, 4, *intent.Request.N)
	assert.Equal(t, "high", intent.Request.Quality)
	assert.Equal(t, "1024x1024", intent.Request.Size)
}

func TestDetectJSONReadsImageParametersFromTool(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol","input":"draw a poster",
		"tools":[{"type":"image_generation","n":3,"quality":"low","size":"9:16"}]
	}`)

	intent, ok, err := DetectJSON("/v1/responses", body)

	require.NoError(t, err)
	require.True(t, ok)
	require.NotNil(t, intent.Request.N)
	assert.EqualValues(t, 3, *intent.Request.N)
	assert.Equal(t, "high", intent.Request.Quality)
	assert.Equal(t, "1024x1536", intent.Request.Size)
}

func TestRoutingBodyContainsOnlyTheSmallC2ASelectionPayload(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.6-sol",
		"input":[{"role":"user","content":[
			{"type":"input_text","text":"edit it"},
			{"type":"input_image","image_url":"data:image/png;base64,iVBORw0KGgo="}
		]}],
		"tools":[{"type":"image_generation"}]
	}`)
	intent, ok, err := DetectJSON("/v1/responses", body)
	require.NoError(t, err)
	require.True(t, ok)

	routingBody, err := intent.RoutingBody()

	require.NoError(t, err)
	assert.Contains(t, string(routingBody), `"model":"gpt-image-2"`)
	assert.NotContains(t, string(routingBody), "iVBORw0KGgo")
}
