package relay

import (
	"fmt"
	"strings"
)

type SubscriptionFamily string
type ProviderFamily string
type OperationClass string
type ExecutionBackend string

const (
	SubscriptionGPT         SubscriptionFamily = "gpt"
	SubscriptionGrok        SubscriptionFamily = "grok"
	ProviderOpenAICodex     ProviderFamily     = "openai_codex"
	ProviderXAI             ProviderFamily     = "xai"
	OperationText           OperationClass     = "text"
	OperationVisionText     OperationClass     = "vision_text"
	OperationImageGenerate  OperationClass     = "image_generate"
	OperationImageEdit      OperationClass     = "image_edit"
	OperationImageVariation OperationClass     = "image_variation"
	OperationVideo          OperationClass     = "video"
	OperationTTS            OperationClass     = "tts"
	OperationSTT            OperationClass     = "stt"
	OperationRealtime       OperationClass     = "realtime"
	BackendCPACodex         ExecutionBackend   = "cpa_codex"
	BackendC2AImage         ExecutionBackend   = "c2a_image"
	BackendCPAXAI           ExecutionBackend   = "cpa_xai"
	BackendCPAAntigravity   ExecutionBackend   = "cpa_antigravity"
	ProviderGoogleGemini    ProviderFamily     = "google_gemini"
	ProviderAntigravity     ProviderFamily     = "antigravity"
	ProviderAnthropic       ProviderFamily     = "anthropic"
)

type RouteRequest struct {
	Method             string             `json:"method"`
	SubscriptionFamily SubscriptionFamily `json:"subscription_family"`
	Endpoint           string             `json:"endpoint"`
	RequestedModel     string             `json:"requested_model"`
	ToolChoice         any                `json:"tool_choice"`
	Modalities         []string           `json:"modalities"`
	InputHasImage      bool               `json:"input_has_image"`
	ChannelProvider    ProviderFamily     `json:"channel_provider"`
	Protocol           string             `json:"protocol"`
}

type RouteResult struct {
	SubscriptionFamily SubscriptionFamily `json:"subscription_family"`
	ProviderFamily     ProviderFamily     `json:"provider_family"`
	OperationClass     OperationClass     `json:"operation_class"`
	RequestedModel     string             `json:"requested_model"`
	EffectiveModel     string             `json:"effective_model"`
	ExecutionBackend   ExecutionBackend   `json:"execution_backend"`
	BillingModel       string             `json:"billing_model"`
}

func ClassifyRoute(req RouteRequest) (RouteResult, error) {
	family := req.SubscriptionFamily
	model := strings.ToLower(strings.TrimSpace(req.RequestedModel))
	modelGrok := strings.HasPrefix(model, "grok")
	modelClaude := strings.HasPrefix(model, "claude")
	modelGemini := strings.HasPrefix(model, "gemini")
	modelGPTOss := strings.HasPrefix(model, "gpt-oss")

	if family != "" && family != SubscriptionGPT && family != SubscriptionGrok {
		return RouteResult{}, fmt.Errorf("unsupported subscription family %q", family)
	}
	if family == SubscriptionGPT {
		if modelGrok || modelClaude || modelGemini || modelGPTOss {
			return RouteResult{}, fmt.Errorf("model %q is outside %s subscription scope", req.RequestedModel, family)
		}
	}
	if family == SubscriptionGrok && !modelGrok && model != "" {
		return RouteResult{}, fmt.Errorf("model %q is outside %s subscription scope", req.RequestedModel, family)
	}

	result := RouteResult{
		SubscriptionFamily: family,
		RequestedModel:     req.RequestedModel,
		EffectiveModel:     req.RequestedModel,
		BillingModel:       req.RequestedModel,
	}

	endpoint := strings.TrimSuffix(strings.SplitN(req.Endpoint, "?", 2)[0], "/")
	isImageEndpoint := endpoint == "/v1/images" || strings.HasPrefix(endpoint, "/v1/images/")
	isLegacyTextEdits := endpoint == "/v1/edits" || strings.HasPrefix(endpoint, "/v1/edits/")

	// Determine ProviderFamily
	if req.ChannelProvider != "" {
		result.ProviderFamily = req.ChannelProvider
	} else if family == SubscriptionGrok || modelGrok {
		result.ProviderFamily = ProviderXAI
	} else if modelGemini {
		result.ProviderFamily = ProviderGoogleGemini
	} else if modelClaude {
		result.ProviderFamily = ProviderAnthropic
	} else if modelGPTOss {
		result.ProviderFamily = ProviderAntigravity
	} else if family == SubscriptionGPT || strings.HasPrefix(model, "gpt") || strings.HasPrefix(model, "o1") || strings.HasPrefix(model, "o3") || strings.HasPrefix(model, "chatgpt") || strings.HasPrefix(model, "text-") || strings.HasPrefix(model, "dall-e") || strings.HasPrefix(model, "tts-") || strings.HasPrefix(model, "whisper-") {
		result.ProviderFamily = ProviderOpenAICodex
	} else {
		result.ProviderFamily = ProviderOpenAICodex
	}

	// Route XAI / Grok
	if result.ProviderFamily == ProviderXAI || family == SubscriptionGrok || modelGrok {
		result.ExecutionBackend = BackendCPAXAI
		switch {
		case strings.Contains(endpoint, "/videos"):
			result.OperationClass = OperationVideo
		case strings.Contains(endpoint, "/audio"):
			result.OperationClass = OperationTTS
		case isImageEndpoint || strings.Contains(model, "imagine-image"):
			result.OperationClass = OperationImageGenerate
		case req.InputHasImage:
			result.OperationClass = OperationVisionText
		default:
			result.OperationClass = OperationText
		}
		return result, nil
	}

	// Route Gemini / Antigravity / Anthropic
	if result.ProviderFamily == ProviderGoogleGemini || result.ProviderFamily == ProviderAntigravity || result.ProviderFamily == ProviderAnthropic || modelGemini || modelClaude || modelGPTOss {
		result.ExecutionBackend = BackendCPAAntigravity
		imageOutput := isImageEndpoint || strings.Contains(model, "-image") || imageToolSelected(req.ToolChoice) || hasImageModality(req.Modalities)
		if imageOutput && (result.ProviderFamily == ProviderGoogleGemini || modelGemini) {
			result.OperationClass = OperationImageGenerate
			result.EffectiveModel = "gemini-3.1-flash-image"
			result.BillingModel = "gemini-3.1-flash-image"
			return result, nil
		}
		if req.InputHasImage {
			result.OperationClass = OperationVisionText
		} else {
			result.OperationClass = OperationText
		}
		return result, nil
	}

	// Route OpenAI / Codex audio / realtime / legacy text edits
	if strings.Contains(endpoint, "/audio/speech") {
		result.OperationClass = OperationTTS
		result.ExecutionBackend = BackendCPACodex
		return result, nil
	}
	if strings.Contains(endpoint, "/audio/") {
		result.OperationClass = OperationSTT
		result.ExecutionBackend = BackendCPACodex
		return result, nil
	}
	if strings.Contains(endpoint, "/realtime") {
		result.OperationClass = OperationRealtime
		result.ExecutionBackend = BackendCPACodex
		return result, nil
	}
	if isLegacyTextEdits {
		result.OperationClass = OperationText
		result.ExecutionBackend = BackendCPACodex
		return result, nil
	}

	// Route OpenAI / Codex image vs text
	imageOutput := isImageEndpoint || strings.HasPrefix(model, "gpt-image") || imageToolSelected(req.ToolChoice) || hasImageModality(req.Modalities)
	if imageOutput {
		result.OperationClass, result.ExecutionBackend = OperationImageGenerate, BackendC2AImage
		if strings.HasSuffix(endpoint, "/edits") {
			result.OperationClass = OperationImageEdit
		} else if strings.HasSuffix(endpoint, "/variations") {
			result.OperationClass = OperationImageVariation
		}
		if strings.HasPrefix(model, "gpt-image") || result.RequestedModel == "" {
			result.EffectiveModel, result.BillingModel = "gpt-image-2", "gpt-image-2"
		}
		return result, nil
	}

	result.ExecutionBackend = BackendCPACodex
	if req.InputHasImage {
		result.OperationClass = OperationVisionText
	} else {
		result.OperationClass = OperationText
	}
	return result, nil
}

func imageToolSelected(choice any) bool {
	if text, ok := choice.(string); ok {
		return strings.EqualFold(text, "image_generation") || strings.EqualFold(text, "__newapi_generate_gpt_image")
	}
	if item, ok := choice.(map[string]any); ok {
		name := fmt.Sprint(item["name"])
		toolType := fmt.Sprint(item["type"])
		if strings.EqualFold(name, "image_generation") || strings.EqualFold(name, "__newapi_generate_gpt_image") ||
			strings.EqualFold(toolType, "image_generation") || strings.EqualFold(toolType, "__newapi_generate_gpt_image") {
			return true
		}
		if fn, ok := item["function"].(map[string]any); ok {
			fnName := fmt.Sprint(fn["name"])
			return strings.EqualFold(fnName, "image_generation") || strings.EqualFold(fnName, "__newapi_generate_gpt_image")
		}
	}
	return false
}

func hasImageModality(modalities []string) bool {
	for _, modality := range modalities {
		if strings.EqualFold(modality, "image") {
			return true
		}
	}
	return false
}
