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
)

type RouteRequest struct {
	SubscriptionFamily SubscriptionFamily
	Endpoint           string
	RequestedModel     string
	ToolChoice         any
	Modalities         []string
	InputHasImage      bool
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
	if family != SubscriptionGPT && family != SubscriptionGrok {
		return RouteResult{}, fmt.Errorf("unsupported subscription family %q", family)
	}
	if (family == SubscriptionGPT && modelGrok) || (family == SubscriptionGrok && !modelGrok && model != "") {
		return RouteResult{}, fmt.Errorf("model %q is outside %s subscription scope", req.RequestedModel, family)
	}
	result := RouteResult{SubscriptionFamily: family, RequestedModel: req.RequestedModel, EffectiveModel: req.RequestedModel, BillingModel: req.RequestedModel}
	if family == SubscriptionGrok {
		result.ProviderFamily, result.ExecutionBackend = ProviderXAI, BackendCPAXAI
		switch {
		case strings.Contains(req.Endpoint, "/videos"):
			result.OperationClass = OperationVideo
		case strings.Contains(req.Endpoint, "/audio"):
			result.OperationClass = OperationTTS
		case strings.Contains(req.Endpoint, "/images") || strings.Contains(model, "imagine-image"):
			result.OperationClass = OperationImageGenerate
		default:
			result.OperationClass = OperationText
		}
		return result, nil
	}
	result.ProviderFamily = ProviderOpenAICodex
	if strings.Contains(req.Endpoint, "/audio/speech") {
		result.OperationClass = OperationTTS
		result.ExecutionBackend = BackendCPACodex
		return result, nil
	}
	if strings.Contains(req.Endpoint, "/audio/") {
		result.OperationClass = OperationSTT
		result.ExecutionBackend = BackendCPACodex
		return result, nil
	}
	if strings.Contains(req.Endpoint, "/realtime") {
		result.OperationClass = OperationRealtime
		result.ExecutionBackend = BackendCPACodex
		return result, nil
	}
	imageOutput := strings.Contains(req.Endpoint, "/images/") || strings.Contains(req.Endpoint, "/edits") || strings.HasPrefix(model, "gpt-image") || imageToolSelected(req.ToolChoice) || hasImageModality(req.Modalities)
	if imageOutput {
		result.OperationClass, result.ExecutionBackend = OperationImageGenerate, BackendC2AImage
		if strings.Contains(req.Endpoint, "/edits") {
			result.OperationClass = OperationImageEdit
		}
		if strings.Contains(req.Endpoint, "/variations") {
			result.OperationClass = OperationImageVariation
		}
		result.EffectiveModel, result.BillingModel = "gpt-image-2", "gpt-image-2"
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
