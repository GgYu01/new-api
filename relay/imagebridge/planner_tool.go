package imagebridge

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
)

const PlannerImageToolName = "__newapi_generate_gpt_image"

// RewriteAutoImageTool replaces the provider-native image tool with a private
// function understood by NewAPI. It is intentionally a semantic JSON rewrite:
// prompt/content values are not inspected or truncated. The caller must only
// invoke it for an authenticated GPT request with tool_choice auto/omitted.
// Chat Completions and Responses use different function-tool encodings, so the
// rewrite follows the request envelope instead of reusing the Chat shape.
//
// The common case — a request without image tools — is decided with cheap
// gjson probes; the full map decode/encode round-trip runs only when a
// rewrite is actually needed.
func RewriteAutoImageTool(body []byte) ([]byte, bool, error) {
	return RewriteAutoImageToolForEnvelope(body, EnvelopeChat)
}

// rewriteNeeded decides whether the auto-image rewrite applies, using only
// cheap gjson probes. It must stay decision-identical to the map-based scan
// in RewriteAutoImageToolForEnvelope (isImageTool + planner veto): the shared
// contract is
//
//	plannerSeen = any tool where functionToolName == PlannerImageToolName
//	              (type=="function", flat or nested name)
//	              or function.name == PlannerImageToolName (any type)
//	imageSeen   = any tool where type == "image_generation"
//	              or function.name == "image_generation" (any type)
//	              or type=="function" with flat name "image_generation"
//
// and the decision is imageSeen && !plannerSeen after the tool_choice guard.
// The planner veto scans every tool, so the decision never depends on array
// order.
func rewriteNeeded(body []byte) bool {
	if choice := gjson.GetBytes(body, "tool_choice"); choice.Exists() {
		if text := choice.String(); choice.Type == gjson.String {
			if text != "" && text != "auto" {
				return false
			}
		} else if choice.IsObject() {
			return false
		}
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return false
	}
	imageTool := false
	plannerTool := false
	tools.ForEach(func(_, item gjson.Result) bool {
		if toolType := item.Get("type"); toolType.Type == gjson.String {
			switch toolType.String() {
			case "image_generation":
				imageTool = true
			case "function":
				if name, ok := functionToolNameGjson(item); ok {
					if name == PlannerImageToolName {
						plannerTool = true
					} else if name == "image_generation" {
						imageTool = true
					}
				}
			}
		}
		if function := item.Get("function"); function.IsObject() {
			if name := function.Get("name"); name.Type == gjson.String {
				switch name.String() {
				case PlannerImageToolName:
					plannerTool = true
				case "image_generation":
					imageTool = true
				}
			}
		}
		return !imageTool || !plannerTool
	})
	return imageTool && !plannerTool
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

func RewriteAutoImageToolForEnvelope(body []byte, envelope Envelope) ([]byte, bool, error) {
	if !rewriteNeeded(body) {
		return body, false, nil
	}
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
	// target ambiguous. Mirrors the planner veto in rewriteNeeded, including
	// function-object tools without a "type" field.
	for _, raw := range tools {
		if name, ok := functionToolName(raw); ok && name == PlannerImageToolName {
			return body, false, nil
		}
		if function, ok := raw.(map[string]any); ok {
			if fn, ok := function["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok && name == PlannerImageToolName {
					return body, false, nil
				}
			}
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
	// Flat Responses-style encoding: {"type":"function","name":"..."}.
	if name, ok := functionToolName(tool); ok && name == "image_generation" {
		return true
	}
	return false
}
