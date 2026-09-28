package imagebridge

import (
	"bytes"
	"encoding/json"

	"github.com/tidwall/gjson"
)

const PlannerImageToolName = "__newapi_generate_gpt_image"

func plannerImageToolParameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"prompt": map[string]any{
				"type":        "string",
				"description": "The description of the image to generate or edit",
			},
			"size": map[string]any{
				"type":        "string",
				"enum":        []string{"1024x1024", "1024x1792", "1792x1024", "512x512", "256x256"},
				"description": "Output dimensions for generated images",
			},
			"quality": map[string]any{
				"type":        "string",
				"enum":        []string{"standard", "hd"},
				"description": "Quality tier for the generated image",
			},
			"n": map[string]any{
				"type":        "integer",
				"description": "Number of images to generate",
			},
			"image": map[string]any{
				"type":        "string",
				"description": "Base image reference, URL, or file ID for editing or variations",
			},
			"mask": map[string]any{
				"type":        "string",
				"description": "Mask image reference for inpainting edits",
			},
			"output_format": map[string]any{
				"type":        "string",
				"enum":        []string{"png", "jpeg", "webp"},
				"description": "Desired output image encoding format",
			},
			"background": map[string]any{
				"type":        "string",
				"description": "Background description or style",
			},
		},
		"required": []string{"prompt"},
	}
}

// RewriteAutoImageTool replaces the provider-native image tool with a private
// function understood by NewAPI.
func RewriteAutoImageTool(body []byte) ([]byte, bool, error) {
	return RewriteAutoImageToolForEnvelope(body, EnvelopeChat)
}

func rewriteNeeded(body []byte) bool {
	if choice := gjson.GetBytes(body, "tool_choice"); choice.Exists() {
		if text := choice.String(); choice.Type == gjson.String {
			if text == "none" {
				return false
			}
		}
	}

	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	hasNativeImageTool := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if toolType := item.Get("type"); toolType.Type == gjson.String {
			switch toolType.String() {
			case "image_generation":
				hasNativeImageTool = true
				return false
			case "function":
				if name, ok := functionToolNameGjson(item); ok && name == "image_generation" {
					hasNativeImageTool = true
					return false
				}
			}
		}
		if function := item.Get("function"); function.IsObject() {
			if name := function.Get("name"); name.Type == gjson.String && name.String() == "image_generation" {
				hasNativeImageTool = true
				return false
			}
		}
		return true
	})
	return hasNativeImageTool
}

func functionToolNameGjson(tool gjson.Result) (string, bool) {
	if tool.Get("type").String() != "function" {
		return "", false
	}
	if name := tool.Get("name"); name.Type == gjson.String && name.String() != "" {
		return name.String(), true
	}
	if name := tool.Get("function.name"); name.Type == gjson.String && name.String() != "" {
		return name.String(), true
	}
	return "", false
}

func rewriteToolChoice(choice any) (any, bool) {
	if text := stringValue(choice); text != "" {
		if text == "image_generation" {
			return PlannerImageToolName, true
		}
	} else if choiceObj, ok := choice.(map[string]any); ok {
		changed := false
		if fn, ok := choiceObj["function"].(map[string]any); ok {
			if fnName := stringValue(fn["name"]); fnName == "image_generation" {
				fn["name"] = PlannerImageToolName
				changed = true
			}
		}
		if name := stringValue(choiceObj["name"]); name == "image_generation" {
			choiceObj["name"] = PlannerImageToolName
			changed = true
		}
		if typ := stringValue(choiceObj["type"]); typ == "image_generation" {
			choiceObj["type"] = "function"
			choiceObj["name"] = PlannerImageToolName
			changed = true
		}
		if changed {
			return choiceObj, true
		}
	}
	return choice, false
}

func rewriteTools(tools []any, envelope Envelope) ([]any, bool) {
	plannerToolPresent := false
	for _, raw := range tools {
		if name, ok := functionToolName(raw); ok && name == PlannerImageToolName {
			plannerToolPresent = true
			break
		}
		if function, ok := raw.(map[string]any); ok {
			if fn, ok := function["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok && name == PlannerImageToolName {
					plannerToolPresent = true
					break
				}
			}
		}
	}

	newTools := make([]any, 0, len(tools))
	rewritten := false

	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			newTools = append(newTools, raw)
			continue
		}
		if isImageTool(tool) {
			rewritten = true
			if !plannerToolPresent {
				if envelope == EnvelopeResponses {
					newTools = append(newTools, map[string]any{
						"type":        "function",
						"name":        PlannerImageToolName,
						"description": "Generate, edit, or create variations of images using AI",
						"parameters":  plannerImageToolParameters(),
					})
				} else {
					newTools = append(newTools, map[string]any{
						"type": "function",
						"function": map[string]any{
							"name":        PlannerImageToolName,
							"description": "Generate, edit, or create variations of images using AI",
							"parameters":  plannerImageToolParameters(),
						},
					})
				}
				plannerToolPresent = true
			}
			// Drops duplicate native image tool so native image capability is never exposed to CPA.
			continue
		}
		newTools = append(newTools, tool)
	}
	return newTools, rewritten
}

func RewriteAutoImageToolForEnvelope(body []byte, envelope Envelope) ([]byte, bool, error) {
	if !rewriteNeeded(body) {
		return body, false, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return body, false, nil
	}

	choiceRewritten := false
	if choice, hasChoice := payload["tool_choice"]; hasChoice {
		newChoice, changed := rewriteToolChoice(choice)
		if changed {
			payload["tool_choice"] = newChoice
			choiceRewritten = true
		}
	}

	tools, ok := payload["tools"].([]any)
	if !ok {
		if choiceRewritten {
			rewrittenBody, err := json.Marshal(payload)
			return rewrittenBody, err == nil, err
		}
		return body, false, nil
	}

	newTools, toolsRewritten := rewriteTools(tools, envelope)
	if !choiceRewritten && !toolsRewritten {
		return body, false, nil
	}

	payload["tools"] = newTools
	rewrittenBody, err := json.Marshal(payload)
	if err != nil {
		return body, false, err
	}
	return rewrittenBody, true, nil
}

func functionToolName(raw any) (string, bool) {
	tool, ok := raw.(map[string]any)
	if !ok {
		return "", false
	}
	if stringValue(tool["type"]) != "function" {
		return "", false
	}
	if name := stringValue(tool["name"]); name != "" {
		return name, true
	}
	if function, ok := tool["function"].(map[string]any); ok {
		if name := stringValue(function["name"]); name != "" {
			return name, true
		}
	}
	return "", false
}

func isImageTool(tool map[string]any) bool {
	if text := stringValue(tool["type"]); text == "image_generation" || text == "image_edits" {
		return true
	}
	if function, ok := tool["function"].(map[string]any); ok {
		if name := stringValue(function["name"]); name == "image_generation" || name == "image_edits" {
			return true
		}
	}
	// Flat Responses-style encoding: {"type":"function","name":"..."}.
	if name, ok := functionToolName(tool); ok && (name == "image_generation" || name == "image_edits") {
		return true
	}
	return false
}
