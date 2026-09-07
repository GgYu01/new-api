package relay

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestClassifyRouteProviderAndOperationMatrix(t *testing.T) {
	tests := []struct {
		name      string
		request   RouteRequest
		backend   ExecutionBackend
		operation OperationClass
	}{
		{"sol text", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/chat/completions", RequestedModel: "gpt-5.6-sol"}, BackendCPACodex, OperationText},
		{"vision input", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/responses", RequestedModel: "gpt-5.6-terra", InputHasImage: true}, BackendCPACodex, OperationVisionText},
		{"gpt image", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/images/generations", RequestedModel: "gpt-image-1"}, BackendC2AImage, OperationImageGenerate},
		{"gpt image edit", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/images/edits", RequestedModel: "gpt-image-1"}, BackendC2AImage, OperationImageEdit},
		{"gpt legacy edit", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/edits", RequestedModel: "text-davinci-edit-001"}, BackendCPACodex, OperationText},
		{"gpt image variation", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/images/variations", RequestedModel: "gpt-image-1"}, BackendC2AImage, OperationImageVariation},
		{"mixed auto", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/responses", RequestedModel: "gpt-5.6-luna"}, BackendCPACodex, OperationText},
		{"forced image tool", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/responses", RequestedModel: "gpt-5.6-luna", ToolChoice: "image_generation"}, BackendC2AImage, OperationImageGenerate},
		{"internal planner image tool", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/responses", RequestedModel: "gpt-5.6-luna", ToolChoice: "__newapi_generate_gpt_image"}, BackendC2AImage, OperationImageGenerate},
		{"audio speech", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/audio/speech", RequestedModel: "tts-1"}, BackendCPACodex, OperationTTS},
		{"audio transcribe", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/audio/transcriptions", RequestedModel: "whisper-1"}, BackendCPACodex, OperationSTT},
		{"realtime", RouteRequest{SubscriptionFamily: SubscriptionGPT, Endpoint: "/v1/realtime", RequestedModel: "gpt-4o-realtime-preview"}, BackendCPACodex, OperationRealtime},
		{"grok text", RouteRequest{SubscriptionFamily: SubscriptionGrok, Endpoint: "/v1/chat/completions", RequestedModel: "grok-4.3"}, BackendCPAXAI, OperationText},
		{"grok image", RouteRequest{SubscriptionFamily: SubscriptionGrok, Endpoint: "/v1/images/generations", RequestedModel: "grok-imagine-image"}, BackendCPAXAI, OperationImageGenerate},
		{"grok video", RouteRequest{SubscriptionFamily: SubscriptionGrok, Endpoint: "/v1/videos", RequestedModel: "grok-imagine-video"}, BackendCPAXAI, OperationVideo},
		{"unrestricted general text", RouteRequest{SubscriptionFamily: "", Endpoint: "/v1/chat/completions", RequestedModel: "custom-llm"}, BackendCPACodex, OperationText},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := ClassifyRoute(test.request)
			require.NoError(t, err)
			require.Equal(t, test.backend, result.ExecutionBackend)
			require.Equal(t, test.operation, result.OperationClass)
		})
	}
}

func TestClassifyRouteRejectsCrossSubscriptionModels(t *testing.T) {
	_, err := ClassifyRoute(RouteRequest{SubscriptionFamily: SubscriptionGPT, RequestedModel: "grok-4.3"})
	require.Error(t, err)
	_, err = ClassifyRoute(RouteRequest{SubscriptionFamily: SubscriptionGrok, RequestedModel: "gpt-5.6-sol"})
	require.Error(t, err)
	// GPT scope must reject non-GPT models: Claude, Gemini, GPT-OSS
	_, err = ClassifyRoute(RouteRequest{SubscriptionFamily: SubscriptionGPT, RequestedModel: "claude-opus-4-6"})
	require.Error(t, err, "GPT subscription must reject Claude models")
	_, err = ClassifyRoute(RouteRequest{SubscriptionFamily: SubscriptionGPT, RequestedModel: "gemini-3-flash"})
	require.Error(t, err, "GPT subscription must reject Gemini models")
	_, err = ClassifyRoute(RouteRequest{SubscriptionFamily: SubscriptionGPT, RequestedModel: "gpt-oss-120b"})
	require.Error(t, err, "GPT subscription must reject GPT-OSS models")
}

func TestClassifyRouteGeminiAndAntigravityProviders(t *testing.T) {
	// 1. Gemini image generation -> gemini-3.1-flash-image on BackendCPAAntigravity (NOT C2A!)
	res, err := ClassifyRoute(RouteRequest{
		Endpoint:        "/v1/images/generations",
		RequestedModel: "gemini-3.1-flash-image-preview",
		ChannelProvider: ProviderGoogleGemini,
	})
	require.NoError(t, err)
	require.Equal(t, BackendCPAAntigravity, res.ExecutionBackend)
	require.Equal(t, OperationImageGenerate, res.OperationClass)
	require.Equal(t, "gemini-3.1-flash-image", res.EffectiveModel)
	require.Equal(t, "gemini-3.1-flash-image", res.BillingModel)

	// 2. Gemini vision text -> BackendCPAAntigravity (NOT Codex!)
	res, err = ClassifyRoute(RouteRequest{
		Endpoint:       "/v1/chat/completions",
		RequestedModel: "gemini-3-flash",
		InputHasImage:  true,
	})
	require.NoError(t, err)
	require.Equal(t, BackendCPAAntigravity, res.ExecutionBackend)
	require.Equal(t, OperationVisionText, res.OperationClass)

	// 3. Unrestricted Grok -> BackendCPAXAI (NOT Codex!)
	res, err = ClassifyRoute(RouteRequest{
		SubscriptionFamily: "",
		Endpoint:           "/v1/chat/completions",
		RequestedModel:     "grok-4.6",
	})
	require.NoError(t, err)
	require.Equal(t, BackendCPAXAI, res.ExecutionBackend)
	require.Equal(t, OperationText, res.OperationClass)

	// 4. Antigravity GPT-OSS -> BackendCPAAntigravity (NOT Codex!)
	res, err = ClassifyRoute(RouteRequest{
		SubscriptionFamily: "",
		Endpoint:           "/v1/chat/completions",
		RequestedModel:     "gpt-oss-120b",
	})
	require.NoError(t, err)
	require.Equal(t, BackendCPAAntigravity, res.ExecutionBackend)
	require.Equal(t, OperationText, res.OperationClass)

	// 5. Antigravity Claude -> BackendCPAAntigravity (NOT Codex!)
	res, err = ClassifyRoute(RouteRequest{
		SubscriptionFamily: "",
		Endpoint:           "/v1/chat/completions",
		RequestedModel:     "claude-sonnet-4-6",
	})
	require.NoError(t, err)
	require.Equal(t, BackendCPAAntigravity, res.ExecutionBackend)
	require.Equal(t, OperationText, res.OperationClass)
}

