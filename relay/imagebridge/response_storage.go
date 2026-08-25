package imagebridge

import (
	"fmt"
	"io"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

// ImageResponseView keeps only bounded response metadata. Large JSON strings
// remain in BodyStorage and are referenced by byte range.
type ImageResponseView struct {
	payload   map[string]any
	b64       *scannedString
	dataURL   *scannedString
	revised   *scannedString
	usage     any
	errorBody any
	dataCount int64
}

func ParseImageResponse(reader io.Reader) (*ImageResponseView, error) {
	parser := newJSONStreamParser(reader)
	value, err := parser.parseDocument()
	if err != nil {
		return nil, err
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("image response top-level value must be an object")
	}
	view := &ImageResponseView{
		payload:   payload,
		usage:     payload["usage"],
		errorBody: payload["error"],
	}
	items, _ := payload["data"].([]any)
	view.dataCount = int64(len(items))
	if len(items) > 0 {
		if first, ok := items[0].(map[string]any); ok {
			view.b64 = scannedStringPointer(first["b64_json"])
			view.revised = scannedStringPointer(first["revised_prompt"])
			url := scannedStringPointer(first["url"])
			if url != nil && strings.HasPrefix(strings.ToLower(strings.TrimSpace(url.value)), "data:") {
				view.dataURL = url
			}
		}
	}
	return view, nil
}

func scannedStringPointer(value any) *scannedString {
	switch token := value.(type) {
	case scannedString:
		copyToken := token
		return &copyToken
	case *scannedString:
		return token
	default:
		return nil
	}
}

func (v *ImageResponseView) DataCount() int64 {
	if v == nil {
		return 0
	}
	return v.dataCount
}

func (v *ImageResponseView) HasImageData() bool {
	if v == nil {
		return false
	}
	return (v.b64 != nil && v.b64.rawLength > 0) || (v.dataURL != nil && v.dataURL.rawLength > 0)
}

// MetadataJSON returns only the small fields required for error handling,
// billing and provider-specific usage normalization.
func (v *ImageResponseView) MetadataJSON() ([]byte, error) {
	if v == nil {
		return []byte(`{}`), nil
	}
	metadata := map[string]any{}
	for _, key := range []string{"usage", "error", "timings", "choices"} {
		if value, exists := v.payload[key]; exists {
			metadata[key] = plainJSONValue(value)
		}
	}
	return common.Marshal(metadata)
}

func plainJSONValue(value any) any {
	switch node := value.(type) {
	case scannedString:
		return node.value
	case *scannedString:
		if node == nil {
			return nil
		}
		return node.value
	case []any:
		result := make([]any, len(node))
		for index, item := range node {
			result[index] = plainJSONValue(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(node))
		for key, item := range node {
			result[key] = plainJSONValue(item)
		}
		return result
	default:
		return value
	}
}

// WriteEnvelopeStorage writes an Images/Responses/Chat envelope directly from
// BodyStorage. Base64 is copied in 32-KiB chunks and is never decoded or
// materialized as another Go string.
func WriteEnvelopeStorage(writer io.Writer, intent Intent, storage common.BodyStorage, ids IDs, view *ImageResponseView) error {
	if intent.Envelope == EnvelopeImages {
		reader, err := storage.NewReader()
		if err != nil {
			return err
		}
		defer reader.Close()
		_, err = io.CopyBuffer(writer, reader, make([]byte, 32<<10))
		return err
	}
	if view == nil || !view.HasImageData() {
		return ErrNoImageData
	}
	if ids.Response == "" || ids.Item == "" || ids.Created == 0 {
		ids = NewIDs(ids.Response)
	}
	model := intent.ClientModel
	if model == "" {
		model = DefaultModel
	}
	usageRaw := ""
	if view.usage != nil {
		encoded, err := common.Marshal(plainJSONValue(view.usage))
		if err != nil {
			return err
		}
		usageRaw = string(encoded)
	}

	switch intent.Envelope {
	case EnvelopeResponses:
		if intent.Stream {
			return writeStoredResponsesStream(writer, storage, view, ids, model, usageRaw)
		}
		return writeStoredCompletedResponse(writer, storage, view, ids, model, usageRaw)
	case EnvelopeChat:
		if intent.Stream {
			return writeStoredChatStream(writer, storage, view, ids, model)
		}
		return writeStoredCompletedChat(writer, storage, view, ids, model)
	default:
		return fmt.Errorf("unsupported image bridge envelope %q", intent.Envelope)
	}
}

func writeStoredCompletedResponse(writer io.Writer, storage common.BodyStorage, view *ImageResponseView, ids IDs, model, usageRaw string) error {
	if err := writeString(writer, `{"id":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, ids.Response); err != nil {
		return err
	}
	if err := writeString(writer, `,"object":"response","created_at":`+fmt.Sprint(ids.Created)+`,"status":"completed","model":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, model); err != nil {
		return err
	}
	if err := writeString(writer, `,"output":[`); err != nil {
		return err
	}
	if err := writeStoredCompletedItem(writer, storage, view, ids.Item); err != nil {
		return err
	}
	if err := writeString(writer, `],"error":null`); err != nil {
		return err
	}
	if usageRaw != "" {
		if err := writeString(writer, `,"usage":`+usageRaw); err != nil {
			return err
		}
	}
	return writeString(writer, `}`)
}

func writeStoredCompletedItem(writer io.Writer, storage common.BodyStorage, view *ImageResponseView, itemID string) error {
	if err := writeString(writer, `{"id":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, itemID); err != nil {
		return err
	}
	if err := writeString(writer, `,"type":"image_generation_call","status":"completed"`); err != nil {
		return err
	}
	if view.revised != nil && view.revised.rawLength > 0 {
		if err := writeString(writer, `,"revised_prompt":`); err != nil {
			return err
		}
		if err := writeStoredJSONString(writer, storage, view.revised); err != nil {
			return err
		}
	}
	if err := writeString(writer, `,"result":"`); err != nil {
		return err
	}
	if err := writeStoredB64Content(writer, storage, view); err != nil {
		return err
	}
	return writeString(writer, `"}`)
}

func writeStoredCompletedChat(writer io.Writer, storage common.BodyStorage, view *ImageResponseView, ids IDs, model string) error {
	suffix := strings.TrimPrefix(ids.Response, "resp_imgbridge_")
	suffix = strings.TrimPrefix(suffix, "resp_")
	if err := writeString(writer, `{"id":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, "chatcmpl_"+suffix); err != nil {
		return err
	}
	if err := writeString(writer, `,"object":"chat.completion","created":`+fmt.Sprint(ids.Created)+`,"model":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, model); err != nil {
		return err
	}
	if err := writeString(writer, `,"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`); err != nil {
		return err
	}
	if err := writeStoredB64Content(writer, storage, view); err != nil {
		return err
	}
	return writeString(writer, `"}}]},"finish_reason":"stop"}]}`)
}

func writeStoredChatStream(writer io.Writer, storage common.BodyStorage, view *ImageResponseView, ids IDs, model string) error {
	suffix := strings.TrimPrefix(ids.Response, "resp_imgbridge_")
	suffix = strings.TrimPrefix(suffix, "resp_")
	chatID := "chatcmpl_" + suffix
	if err := writeString(writer, `data: {"id":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, chatID); err != nil {
		return err
	}
	if err := writeString(writer, `,"object":"chat.completion.chunk","created":`+fmt.Sprint(ids.Created)+`,"model":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, model); err != nil {
		return err
	}
	if err := writeString(writer, `,"choices":[{"index":0,"delta":{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`); err != nil {
		return err
	}
	if err := writeStoredB64Content(writer, storage, view); err != nil {
		return err
	}
	if err := writeString(writer, `"}}]},"finish_reason":null}]}`+"\n\n"); err != nil {
		return err
	}
	if err := writeString(writer, `data: {"id":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, chatID); err != nil {
		return err
	}
	if err := writeString(writer, `,"object":"chat.completion.chunk","created":`+fmt.Sprint(ids.Created)+`,"model":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, model); err != nil {
		return err
	}
	return writeString(writer, `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`+"\n\n"+"data: [DONE]\n\n")
}

func writeStoredResponsesStream(writer io.Writer, storage common.BodyStorage, view *ImageResponseView, ids IDs, model, usageRaw string) error {
	modelRaw, err := common.Marshal(model)
	if err != nil {
		return err
	}
	responseRaw, err := common.Marshal(ids.Response)
	if err != nil {
		return err
	}
	itemRaw, err := common.Marshal(ids.Item)
	if err != nil {
		return err
	}
	created := fmt.Sprint(ids.Created)
	inProgress := `{"id":` + string(responseRaw) + `,"object":"response","created_at":` + created + `,"status":"in_progress","model":` + string(modelRaw) + `,"output":[],"error":null}`
	events := []struct {
		name string
		data string
	}{
		{"response.created", `{"response":` + inProgress + `,"type":"response.created","sequence_number":1}`},
		{"response.in_progress", `{"response":` + inProgress + `,"type":"response.in_progress","sequence_number":2}`},
		{"response.output_item.added", `{"output_index":0,"item":{"id":` + string(itemRaw) + `,"type":"image_generation_call","status":"in_progress"},"type":"response.output_item.added","sequence_number":3}`},
		{"response.image_generation_call.in_progress", `{"output_index":0,"item_id":` + string(itemRaw) + `,"type":"response.image_generation_call.in_progress","sequence_number":4}`},
		{"response.image_generation_call.generating", `{"output_index":0,"item_id":` + string(itemRaw) + `,"type":"response.image_generation_call.generating","sequence_number":5}`},
		{"response.image_generation_call.completed", `{"output_index":0,"item_id":` + string(itemRaw) + `,"type":"response.image_generation_call.completed","sequence_number":6}`},
	}
	for _, event := range events {
		if err := writeString(writer, "event: "+event.name+"\ndata: "+event.data+"\n\n"); err != nil {
			return err
		}
	}
	if err := writeString(writer, `event: response.output_item.done`+"\n"+`data: {"output_index":0,"item":`); err != nil {
		return err
	}
	if err := writeStoredCompletedItem(writer, storage, view, ids.Item); err != nil {
		return err
	}
	if err := writeString(writer, `,"type":"response.output_item.done","sequence_number":7}`+"\n\n"); err != nil {
		return err
	}
	if err := writeString(writer, `event: response.completed`+"\n"+`data: {"response":`); err != nil {
		return err
	}
	if err := writeStoredCompletedResponse(writer, storage, view, ids, model, usageRaw); err != nil {
		return err
	}
	if err := writeString(writer, `,"type":"response.completed","sequence_number":8}`+"\n\n"); err != nil {
		return err
	}
	return writeString(writer, "data: [DONE]\n\n")
}

func writeStoredB64Content(writer io.Writer, storage common.BodyStorage, view *ImageResponseView) error {
	if view.b64 != nil && view.b64.rawLength > 0 {
		return copyStorageRange(writer, storage, view.b64.rawOffset, view.b64.rawLength)
	}
	if view.dataURL == nil {
		return ErrNoImageData
	}
	reader, err := openJSONString(storage, view.dataURL.rawOffset, view.dataURL.rawLength)
	if err != nil {
		return err
	}
	defer reader.Close()
	for {
		buffer := make([]byte, 1)
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return fmt.Errorf("invalid image data URL: %w", err)
		}
		if buffer[0] == ',' {
			break
		}
	}
	_, err = io.CopyBuffer(writer, reader, make([]byte, 32<<10))
	return err
}

func writeStoredJSONString(writer io.Writer, storage common.BodyStorage, token *scannedString) error {
	if err := writeString(writer, `"`); err != nil {
		return err
	}
	if err := copyStorageRange(writer, storage, token.rawOffset, token.rawLength); err != nil {
		return err
	}
	return writeString(writer, `"`)
}

func copyStorageRange(writer io.Writer, storage common.BodyStorage, offset, length int64) error {
	reader, err := storage.NewReader()
	if err != nil {
		return err
	}
	defer reader.Close()
	if seeker, ok := reader.(io.Seeker); ok {
		if _, err := seeker.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	} else if _, err := io.CopyN(io.Discard, reader, offset); err != nil {
		return err
	}
	_, err = io.CopyBuffer(writer, io.LimitReader(reader, length), make([]byte, 32<<10))
	return err
}
