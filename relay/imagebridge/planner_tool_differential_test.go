package imagebridge

import (
	"encoding/json"
	"strings"
	"testing"
)

// referenceScan is an independent expression of the documented shared
// contract between rewriteNeeded (gjson fast path) and the map-based scan in
// RewriteAutoImageToolForEnvelope. It decodes the body with the standard
// library and walks the payload without gjson.
func referenceScan(payload map[string]any) (imageSeen, plannerSeen bool) {
	tools, _ := payload["tools"].([]any)
	for _, raw := range tools {
		tool, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := tool["type"].(string)
		if typ == "image_generation" {
			imageSeen = true
		}
		if fn, ok := tool["function"].(map[string]any); ok {
			if name, _ := fn["name"].(string); name == "image_generation" {
				imageSeen = true
			}
			if name, _ := fn["name"].(string); name == PlannerImageToolName {
				plannerSeen = true
			}
		}
		if typ == "function" {
			if name, _ := tool["name"].(string); name == "image_generation" {
				imageSeen = true
			}
			if name, _ := tool["name"].(string); name == PlannerImageToolName {
				plannerSeen = true
			}
		}
	}
	return imageSeen, plannerSeen
}

func referenceGuard(payload map[string]any) bool {
	choice, present := payload["tool_choice"]
	if !present {
		return true
	}
	switch v := choice.(type) {
	case nil:
		return true
	case string:
		if v == "none" {
			return false
		}
		return true
	case map[string]any:
		return true
	default:
		return true
	}
}

func referenceDecision(body []byte) bool {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return false
	}
	if !referenceGuard(payload) {
		return false
	}
	imageSeen, _ := referenceScan(payload)
	return imageSeen
}

var plannerToolForms = []string{
	`{"type":"image_generation"}`,
	`{"type":"image_generation","n":2}`,
	`{"type":"function","function":{"name":"image_generation"}}`,
	`{"type":"function","name":"image_generation"}`,
	`{"function":{"name":"image_generation"}}`,
	`{"type":"function","function":{"name":"weather"}}`,
	`{"type":"function","name":"weather"}`,
	`{"type":"web_search"}`,
	`{"type":"function","function":{"name":"__newapi_generate_gpt_image"}}`,
	`{"type":"function","name":"__newapi_generate_gpt_image"}`,
	`{"function":{"name":"__newapi_generate_gpt_image"}}`,
}

var plannerChoiceForms = []string{
	`"auto"`, `""`, `"image_generation"`, `"required"`,
	`{"type":"function","function":{"name":"weather"}}`,
	`null`,
}

// TestRewriteNeededMatchesMapScanProperty sweeps tool combinations (including
// order swaps) and tool_choice forms through both the gjson fast path and the
// reference map scan; they must agree on every input.
func TestRewriteNeededMatchesMapScanProperty(t *testing.T) {
	for _, a := range plannerToolForms {
		for _, b := range plannerToolForms {
			for _, choice := range plannerChoiceForms {
				body := []byte(`{"model":"gpt-5.6-sol","input":"hi","tools":[` + a + `,` + b + `],"tool_choice":` + choice + `}`)
				want := referenceDecision(body)
				got := rewriteNeeded(body)
				if got != want {
					t.Fatalf("rewriteNeeded=%v reference=%v for tools [%s,%s] choice=%s", got, want, a, b, choice)
				}
			}
		}
	}
}

// TestRewriteNeededToolFormSingle pins single-tool cases from the review
// finding: chat-style and flat function tools named image_generation were
// missed by the fast path before the fix.
func TestRewriteNeededToolFormSingle(t *testing.T) {
	for _, form := range plannerToolForms {
		body := []byte(`{"tools":[` + form + `]}`)
		want := referenceDecision(body)
		got := rewriteNeeded(body)
		if got != want {
			t.Fatalf("rewriteNeeded=%v reference=%v for %s", got, want, form)
		}
	}
}

// TestRewriteEnvelopeOutputShape checks that whenever the fast path says a
// rewrite is needed, the slow path actually rewrites into the envelope-correct
// planner form and leaves no native image tool behind.
func TestRewriteEnvelopeOutputShape(t *testing.T) {
	for _, form := range plannerToolForms {
		for _, envelope := range []Envelope{EnvelopeChat, EnvelopeResponses} {
			body := []byte(`{"model":"gpt-5.6-sol","input":"hi","tools":[` + form + `],"tool_choice":"auto"}`)
			if !rewriteNeeded(body) {
				continue
			}
			out, changed, err := RewriteAutoImageToolForEnvelope(body, envelope)
			if err != nil {
				t.Fatalf("rewrite(%s, %s) error: %v", form, envelope, err)
			}
			if !changed {
				t.Fatalf("rewriteNeeded=true but rewrite(%s, %s) made no change", form, envelope)
			}
			text := string(out)
			if !strings.Contains(text, PlannerImageToolName) {
				t.Fatalf("rewritten body missing planner tool for %s/%s: %s", form, envelope, text)
			}
			if strings.Contains(text, `"image_generation"`) {
				t.Fatalf("rewritten body still names image_generation for %s/%s: %s", form, envelope, text)
			}
			var parsed map[string]any
			if err := json.Unmarshal(out, &parsed); err != nil {
				t.Fatalf("rewritten body is not valid JSON: %v", err)
			}
			tools, _ := parsed["tools"].([]any)
			if len(tools) == 0 {
				t.Fatalf("missing tools in rewritten body: %s", text)
			}
			tool0, _ := tools[0].(map[string]any)
			if envelope == EnvelopeChat {
				fn, _ := tool0["function"].(map[string]any)
				if fn == nil || fn["name"] != PlannerImageToolName {
					t.Fatalf("chat envelope must nest planner under function: %s", text)
				}
			}
			if envelope == EnvelopeResponses {
				if tool0["name"] != PlannerImageToolName {
					t.Fatalf("responses envelope must carry flat planner name: %s", text)
				}
			}
		}
	}
}

// TestRewriteDeduplicatesAndEliminatesNativeImageWhenPlannerPresent tests that
// native image tools are never exposed to CPA even when a planner tool is already present.
func TestRewriteDeduplicatesAndEliminatesNativeImageWhenPlannerPresent(t *testing.T) {
	image := `{"type":"image_generation"}`
	planner := `{"type":"function","function":{"name":"__newapi_generate_gpt_image"}}`
	for _, body := range [][]byte{
		[]byte(`{"tools":[` + image + `,` + planner + `],"tool_choice":"auto"}`),
		[]byte(`{"tools":[` + planner + `,` + image + `],"tool_choice":"auto"}`),
	} {
		if !rewriteNeeded(body) {
			t.Fatalf("rewrite should be needed to eliminate native image tool for %s", body)
		}
		rewritten, changed, err := RewriteAutoImageToolForEnvelope(body, EnvelopeChat)
		if err != nil || !changed {
			t.Fatalf("rewrite failed: changed=%v err=%v", changed, err)
		}
		text := string(rewritten)
		if strings.Contains(text, `"image_generation"`) {
			t.Fatalf("native image tool must be purged from tools array: %s", text)
		}
		if !strings.Contains(text, PlannerImageToolName) {
			t.Fatalf("planner tool must remain in tools array: %s", text)
		}
	}
}

func TestRewriteAutoImageToolPreservesLargeIntegers(t *testing.T) {
	body := []byte(`{"id":18446744073709551615,"model":"gpt-5.6-sol","tools":[{"type":"image_generation"}],"tool_choice":"auto"}`)
	rewritten, changed, err := RewriteAutoImageToolForEnvelope(body, EnvelopeChat)
	if err != nil || !changed {
		t.Fatalf("rewrite failed: changed=%v err=%v", changed, err)
	}
	text := string(rewritten)
	if !strings.Contains(text, "18446744073709551615") {
		t.Fatalf("large integer 18446744073709551615 was corrupted or mangled: %s", text)
	}
}

func TestRewriteAutoImageToolForcedToolChoice(t *testing.T) {
	body := []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":{"type":"function","function":{"name":"image_generation"}}}`)
	rewritten, changed, err := RewriteAutoImageToolForEnvelope(body, EnvelopeChat)
	if err != nil || !changed {
		t.Fatalf("rewrite failed: changed=%v err=%v", changed, err)
	}
	text := string(rewritten)
	if strings.Contains(text, `"image_generation"`) {
		t.Fatalf("forced choice image_generation was not rewritten: %s", text)
	}
	if !strings.Contains(text, PlannerImageToolName) {
		t.Fatalf("forced choice must name planner tool: %s", text)
	}
}

func TestRewriteAutoImageToolParametersSchema(t *testing.T) {
	body := []byte(`{"tools":[{"type":"image_generation"}],"tool_choice":"auto"}`)
	rewritten, changed, err := RewriteAutoImageToolForEnvelope(body, EnvelopeResponses)
	if err != nil || !changed {
		t.Fatalf("rewrite failed: changed=%v err=%v", changed, err)
	}
	text := string(rewritten)
	for _, expectedProp := range []string{"prompt", "size", "quality", "n", "image", "mask", "output_format"} {
		if !strings.Contains(text, expectedProp) {
			t.Fatalf("parameters schema missing property %q: %s", expectedProp, text)
		}
	}
}
