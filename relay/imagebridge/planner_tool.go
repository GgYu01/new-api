package imagebridge

import "github.com/QuantumNous/new-api/common"

const PlannerImageToolName = "__newapi_generate_gpt_image"

// RewriteAutoImageTool replaces the provider-native image tool with a private
// function understood by NewAPI. It is intentionally a semantic JSON rewrite:
// prompt/content values are not inspected or truncated. The caller must only
// invoke it for an authenticated GPT request with tool_choice auto/omitted.
// Chat Completions and Responses use different function-tool encodings, so the
// rewrite follows the request envelope instead of reusing the Chat shape.
func RewriteAutoImageTool(body []byte) ([]byte, bool, error) {
	return RewriteAutoImageToolForEnvelope(body, EnvelopeChat)
}

func RewriteAutoImageToolForEnvelope(body []byte, envelope Envelope) ([]byte, bool, error) {
	var payload map[string]any
	if err := common.Unmarshal(body, &payload); err != nil {
		return body, false, nil
	}
	choice, hasChoice := payload["tool_choice"]
	if hasChoice {
		if text, ok := choice.(string); ok && text != "" && text != "auto" {
			return body, false, nil
		}
		if _, forced := choice.(map[string]any); forced {
			return body, false, nil
		}
	}
	tools, ok := payload["tools"].([]any)
	if !ok {
		return body, false, nil
	}
	// Never introduce a second definition of the private planner function: a
	// client-defined function with the same name would make the tool call
	// target ambiguous.
	for _, raw := range tools {
		if name, ok := functionToolName(raw); ok && name == PlannerImageToolName {
			return body, false, nil
		}
	}
	rewritten := false
	for index, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok || !isImageTool(tool) {
			continue
		}
		if envelope == EnvelopeResponses {
			// Responses API function tools are flat: name/description/parameters
			// live at the top level, there is no "function" wrapper.
			tools[index] = map[string]any{
				"type":        "function",
				"name":        PlannerImageToolName,
				"description": "Generate or edit a GPT image through NewAPI",
				"parameters":  map[string]any{"type": "object"},
			}
		} else {
			// Chat Completions function tools nest under a "function" object.
			tools[index] = map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        PlannerImageToolName,
					"description": "Generate or edit a GPT image through NewAPI",
					"parameters":  map[string]any{"type": "object"},
				},
			}
		}
		rewritten = true
	}
	if !rewritten {
		return body, false, nil
	}
	payload["tools"] = tools
	rewrittenBody, err := common.Marshal(payload)
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
	if tool["type"] != "function" {
		return "", false
	}
	if name, ok := tool["name"].(string); ok && name != "" {
		return name, true
	}
	if function, ok := tool["function"].(map[string]any); ok {
		if name, ok := function["name"].(string); ok && name != "" {
			return name, true
		}
	}
	return "", false
}

func isImageTool(tool map[string]any) bool {
	if text, ok := tool["type"].(string); ok && text == "image_generation" {
		return true
	}
	if function, ok := tool["function"].(map[string]any); ok {
		if name, ok := function["name"].(string); ok && name == "image_generation" {
			return true
		}
	}
	return false
}
