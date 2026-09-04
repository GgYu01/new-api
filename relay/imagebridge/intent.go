package imagebridge

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

const (
	DefaultModel           = "gpt-image-2"
	DefaultVariationPrompt = "Create a visual variation of the uploaded image while preserving its core subject and composition."
	defaultEditPrompt      = "Edit the uploaded images as requested."
	maxPromptBytes         = 8000
)

var openAIFamilyModelPattern = regexp.MustCompile(`^o\d(?:[-.]|$)`)

type Envelope string

const (
	EnvelopeImages    Envelope = "images"
	EnvelopeResponses Envelope = "responses"
	EnvelopeChat      Envelope = "chat"
)

type Mode string

const (
	ModeGeneration Mode = "generation"
	ModeEdit       Mode = "edit"
)

type Intent struct {
	Envelope    Envelope
	Mode        Mode
	ClientModel string
	Request     dto.ImageRequest
	Stream      bool
	StreamSet   bool
	ImageCount  int
	Source      SourceFormat
	ContentType string
	InputImages []InputImage
}

type SourceFormat string

const (
	SourceJSON      SourceFormat = "json"
	SourceMultipart SourceFormat = "multipart"
)

func (i Intent) UpstreamPath() string {
	if i.Mode == ModeEdit {
		return "/v1/images/edits"
	}
	return "/v1/images/generations"
}

// RoutingBody returns the small request used only for NewAPI channel
// selection. Input images remain in the request-owned BodyStorage and are not
// copied into this payload.
func (i Intent) RoutingBody() ([]byte, error) {
	routing := i.Request
	routing.Image = nil
	routing.Images = nil
	routing.Mask = nil
	routing.Extra = nil
	return common.Marshal(routing)
}

func DetectJSON(path string, body []byte) (Intent, bool, error) {
	return DetectJSONReader(path, bytes.NewReader(body))
}

func detect(path string, payload map[string]any) (Intent, bool, error) {
	normalizedPath := strings.TrimSuffix(strings.SplitN(path, "?", 2)[0], "/")
	clientModel := stringValue(payload["model"])
	envelope := EnvelopeImages
	imageRequest := strings.HasPrefix(normalizedPath, "/v1/images")
	imageTool, hasImageTool := findSelectedImageTool(payload)
	switch normalizedPath {
	case "/v1/responses":
		envelope = EnvelopeResponses
		imageRequest = hasImageTool || isImageModel(clientModel) || hasExplicitImageModality(payload)
	case "/v1/chat/completions":
		envelope = EnvelopeChat
		imageRequest = hasImageTool || isImageModel(clientModel) || hasExplicitImageModality(payload)
	}
	if !imageRequest {
		return Intent{}, false, nil
	}

	prompt, err := extractPrompt(payload)
	if err != nil {
		return Intent{}, false, err
	}
	imageCount := countInputImages(payload)
	mode := ModeGeneration
	if normalizedPath == "/v1/images/variations" {
		mode = ModeEdit
		if prompt == "" {
			prompt = DefaultVariationPrompt
		}
	} else if normalizedPath == "/v1/images/edits" || imageCount > 0 {
		mode = ModeEdit
		if prompt == "" {
			prompt = defaultEditPrompt
		}
	}

	nValue, hasN := payload["n"]
	if !hasN && imageTool != nil {
		nValue = imageTool["n"]
	}
	sizeValue, hasSize := payload["size"]
	if !hasSize && imageTool != nil {
		sizeValue = imageTool["size"]
	}
	n := uint(clampImageCount(nValue))
	requestModel := DefaultModel
	// The image bridge uses DefaultModel only for OpenAI/Codex image
	// conversion. Explicit foreign-provider image models must remain the
	// channel-selection model so they pass through NewAPI/CPA unchanged.
	if clientModel != "" && !isOpenAIFamilyModel(clientModel) {
		requestModel = clientModel
	}
	request := dto.ImageRequest{
		Model:          requestModel,
		Prompt:         prompt,
		N:              &n,
		Size:           requestedSize(sizeValue, prompt),
		Quality:        "high",
		ResponseFormat: "b64_json",
	}
	stream, streamSet := payload["stream"].(bool)
	request.Stream = &stream
	return Intent{
		Envelope:    envelope,
		Mode:        mode,
		ClientModel: clientModel,
		Request:     request,
		Stream:      stream,
		StreamSet:   streamSet,
		ImageCount:  imageCount,
	}, true, nil
}

func isOpenAIFamilyModel(model string) bool {
	normalized := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(normalized, "gpt-") ||
		strings.HasPrefix(normalized, "gpt_image") ||
		strings.HasPrefix(normalized, "gpt-image") ||
		strings.HasPrefix(normalized, "dall-e") ||
		strings.HasPrefix(normalized, "dalle") ||
		strings.HasPrefix(normalized, "codex") ||
		strings.HasPrefix(normalized, "chatgpt") ||
		openAIFamilyModelPattern.MatchString(normalized)
}

func findSelectedImageTool(payload map[string]any) (map[string]any, bool) {
	// An available image_generation tool with tool_choice=auto belongs to the
	// CPA text planner. Only an explicit selection is a direct C2A operation.
	if !toolChoiceIsImage(payload["tool_choice"]) {
		return nil, false
	}
	tools, _ := payload["tools"].([]any)
	for _, tool := range tools {
		if toolChoiceIsImage(tool) {
			if item, ok := tool.(map[string]any); ok {
				return item, true
			}
			return map[string]any{}, true
		}
	}
	return map[string]any{}, true
}

func hasExplicitImageModality(payload map[string]any) bool {
	modalities, ok := payload["modalities"].([]any)
	if !ok {
		return false
	}
	for _, modality := range modalities {
		if strings.EqualFold(stringValue(modality), "image") {
			return true
		}
	}
	return false
}

func toolChoiceIsImage(value any) bool {
	if text := stringValue(value); text != "" {
		return strings.EqualFold(text, "image_generation")
	}
	switch item := value.(type) {
	case map[string]any:
		if strings.EqualFold(stringValue(item["type"]), "image_generation") ||
			strings.EqualFold(stringValue(item["name"]), "image_generation") {
			return true
		}
		if function, ok := item["function"].(map[string]any); ok {
			return strings.EqualFold(stringValue(function["name"]), "image_generation")
		}
	}
	return false
}

func isImageModel(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	for _, prefix := range []string{
		"gpt-image", "dall-e", "dalle", "grok-imagine-image",
		"gemini-3-pro-image", "gemini-3.1-flash-image", "nano-banana",
	} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

func extractPrompt(payload map[string]any) (string, error) {
	if prompt := stringValue(payload["prompt"]); prompt != "" {
		return preservePrompt(prompt)
	}
	var chunks []string
	collectUserText(payload["input"], &chunks, 0)
	if len(chunks) == 0 {
		collectUserText(payload["messages"], &chunks, 0)
	}
	if len(chunks) == 0 {
		if instructions := stringValue(payload["instructions"]); instructions != "" {
			chunks = append(chunks, instructions)
		}
	}
	return preservePrompt(strings.Join(chunks, "\n"))
}

func collectUserText(value any, chunks *[]string, depth int) {
	if depth > 8 || value == nil {
		return
	}
	switch node := value.(type) {
	case string:
		if text := strings.TrimSpace(node); text != "" {
			*chunks = append(*chunks, text)
		}
	case scannedString:
		if text := strings.TrimSpace(node.value); text != "" {
			*chunks = append(*chunks, text)
		}
	case []any:
		for _, item := range node {
			collectUserText(item, chunks, depth+1)
		}
	case map[string]any:
		typeName := strings.ToLower(stringValue(node["type"]))
		if typeName == "input_image" || typeName == "image_url" || typeName == "image" {
			return
		}
		for _, key := range []string{"text", "input_text", "content", "value"} {
			if child, exists := node[key]; exists {
				collectUserText(child, chunks, depth+1)
			}
		}
	}
}

func countInputImages(payload map[string]any) int {
	count := 0
	var walk func(any, int)
	walk = func(value any, depth int) {
		if depth > 8 || count >= 4 || value == nil {
			return
		}
		switch node := value.(type) {
		case []any:
			for _, item := range node {
				walk(item, depth+1)
			}
		case map[string]any:
			typeName := strings.ToLower(stringValue(node["type"]))
			if typeName == "input_image" || typeName == "image_url" || typeName == "image" {
				count++
				return
			}
			for _, key := range []string{"content", "input", "parts", "message"} {
				walk(node[key], depth+1)
			}
		}
	}

	for _, key := range []string{
		"image", "images", "image[]", "images[]", "image_url", "image_urls",
		"file", "files", "input_image", "input_images", "image_base64", "images_base64",
	} {
		if value, exists := payload[key]; exists && value != nil {
			switch items := value.(type) {
			case []any:
				count += len(items)
			default:
				count++
			}
			if count >= 4 {
				return 4
			}
		}
	}
	walk(payload["input"], 0)
	walk(payload["messages"], 0)
	if count > 4 {
		return 4
	}
	return count
}

func clampImageCount(value any) int {
	count := 1
	switch raw := value.(type) {
	case float64:
		count = int(raw)
	case int:
		count = raw
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil {
			count = parsed
		}
	case scannedString:
		if parsed, err := strconv.Atoi(strings.TrimSpace(raw.value)); err == nil {
			count = parsed
		}
	}
	if count < 1 {
		return 1
	}
	if count > 4 {
		return 4
	}
	return count
}

func requestedSize(value any, prompt string) string {
	size := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(stringValue(value)), "*", "x"), " ", ""))
	switch size {
	case "1:1":
		return "1024x1024"
	case "9:16":
		return "1024x1536"
	case "16:9":
		return "1536x1024"
	case "", "auto":
		return inferSize(prompt)
	default:
		return size
	}
}

var explicitSizePattern = regexp.MustCompile(`(?i)(\d{3,4})\s*[x*]\s*(\d{3,4})`)

func inferSize(prompt string) string {
	if match := explicitSizePattern.FindStringSubmatch(prompt); len(match) == 3 {
		return match[1] + "x" + match[2]
	}
	compact := strings.ReplaceAll(prompt, " ", "")
	switch {
	case strings.Contains(compact, "16:9"):
		return "1536x1024"
	case strings.Contains(compact, "9:16"):
		return "1024x1536"
	case strings.Contains(compact, "1:1"):
		return "1024x1024"
	default:
		return ""
	}
}

func preservePrompt(prompt string) (string, error) {
	// Preserve caller whitespace and ordering. Silent strings.Fields flattening
	// and tail truncation can change image semantics; reject oversized prompts
	// with a bounded validation error instead.
	prompt = strings.TrimSpace(prompt)
	if len([]byte(prompt)) > maxPromptBytes {
		return "", fmt.Errorf("image prompt exceeds %d bytes", maxPromptBytes)
	}
	return prompt, nil
}

func stringValue(value any) string {
	switch text := value.(type) {
	case string:
		return strings.TrimSpace(text)
	case scannedString:
		return strings.TrimSpace(text.value)
	case *scannedString:
		if text != nil {
			return strings.TrimSpace(text.value)
		}
	}
	return ""
}
