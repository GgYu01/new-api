package imagebridge

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/tidwall/gjson"
)

var ErrNoImageData = errors.New("image bridge upstream response contains no image data")

type IDs struct {
	Response string
	Item     string
	Created  int64
}

func NewIDs(requestID string) IDs {
	suffix := strings.TrimSpace(requestID)
	if suffix == "" {
		suffix = common.NewRequestId()
	}
	suffix = strings.TrimPrefix(suffix, "resp_")
	return IDs{
		Response: "resp_imgbridge_" + suffix,
		Item:     "ig_imgbridge_" + suffix,
		Created:  time.Now().Unix(),
	}
}

// WriteEnvelope transforms a successful Images API response into the client
// protocol without decoding or re-encoding the base64 image. The large JSON
// string is copied directly from the upstream response.
func WriteEnvelope(writer io.Writer, intent Intent, upstream []byte, ids IDs) error {
	if intent.Envelope == EnvelopeImages {
		_, err := writer.Write(upstream)
		return err
	}

	b64 := gjson.GetBytes(upstream, "data.0.b64_json")
	var b64Raw string
	if b64.Type == gjson.String && b64.String() != "" {
		b64Raw = b64.Raw
	}
	if b64Raw == "" {
		url := gjson.GetBytes(upstream, "data.0.url")
		if url.Type == gjson.String {
			value := url.String()
			if comma := strings.Index(value, ","); strings.HasPrefix(value, "data:") && comma >= 0 && comma+1 < len(value) {
				raw, err := common.Marshal(value[comma+1:])
				if err != nil {
					return err
				}
				b64Raw = string(raw)
			}
		}
	}
	if b64Raw == "" {
		return ErrNoImageData
	}

	if ids.Response == "" || ids.Item == "" || ids.Created == 0 {
		ids = NewIDs(ids.Response)
	}
	model := intent.ClientModel
	if model == "" {
		model = DefaultModel
	}
	revisedRaw := ""
	if revised := gjson.GetBytes(upstream, "data.0.revised_prompt"); revised.Type == gjson.String && revised.String() != "" {
		revisedRaw = revised.Raw
	}
	usageRaw := ""
	if usage := gjson.GetBytes(upstream, "usage"); usage.IsObject() {
		usageRaw = usage.Raw
	}

	switch intent.Envelope {
	case EnvelopeResponses:
		if intent.Stream {
			return writeResponsesStream(writer, ids, model, b64Raw, revisedRaw, usageRaw)
		}
		return writeCompletedResponse(writer, ids, model, b64Raw, revisedRaw, usageRaw)
	case EnvelopeChat:
		if intent.Stream {
			return writeChatStream(writer, ids, model, b64Raw)
		}
		return writeCompletedChat(writer, ids, model, b64Raw)
	default:
		return fmt.Errorf("unsupported image bridge envelope %q", intent.Envelope)
	}
}

// WriteFailure emits the terminal SSE failure used after a bridge heartbeat
// has already committed a 200 response. Before the first downstream byte the
// normal relay error path still returns the upstream HTTP status instead.
func WriteFailure(writer io.Writer, intent Intent, ids IDs, message string) error {
	model := strings.TrimSpace(intent.ClientModel)
	if model == "" {
		model = DefaultModel
	}
	payload, err := common.Marshal(map[string]any{
		"type": "response.failed",
		"response": map[string]any{
			"id":         ids.Response,
			"object":     "response",
			"created_at": ids.Created,
			"status":     "failed",
			"model":      model,
			"output":     []any{},
			"error": map[string]any{
				"message": message,
				"code":    "image_generation_failed",
			},
		},
	})
	if err != nil {
		return err
	}
	if err := writeString(writer, "event: response.failed\ndata: "); err != nil {
		return err
	}
	if _, err := writer.Write(payload); err != nil {
		return err
	}
	return writeString(writer, "\n\ndata: [DONE]\n\n")
}

func writeCompletedResponse(writer io.Writer, ids IDs, model, b64Raw, revisedRaw, usageRaw string) error {
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
	if err := writeCompletedItem(writer, ids.Item, b64Raw, revisedRaw); err != nil {
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

func writeCompletedItem(writer io.Writer, itemID, b64Raw, revisedRaw string) error {
	if err := writeString(writer, `{"id":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, itemID); err != nil {
		return err
	}
	if err := writeString(writer, `,"type":"image_generation_call","status":"completed"`); err != nil {
		return err
	}
	if revisedRaw != "" {
		if err := writeString(writer, `,"revised_prompt":`+revisedRaw); err != nil {
			return err
		}
	}
	return writeString(writer, `,"result":`+b64Raw+`}`)
}

func writeCompletedChat(writer io.Writer, ids IDs, model, b64Raw string) error {
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
	return writeString(writer, `,"choices":[{"index":0,"message":{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+strings.Trim(b64Raw, `"`)+`"}}]},"finish_reason":"stop"}]}`)
}

func writeChatStream(writer io.Writer, ids IDs, model, b64Raw string) error {
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
	if err := writeString(writer, `,"choices":[{"index":0,"delta":{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,`+strings.Trim(b64Raw, `"`)+`"}}]},"finish_reason":null}]}`+"\n\n"); err != nil {
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

func writeResponsesStream(writer io.Writer, ids IDs, model, b64Raw, revisedRaw, usageRaw string) error {
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
	if err := writeCompletedItem(writer, ids.Item, b64Raw, revisedRaw); err != nil {
		return err
	}
	if err := writeString(writer, `,"type":"response.output_item.done","sequence_number":7}`+"\n\n"); err != nil {
		return err
	}

	if err := writeString(writer, `event: response.completed`+"\n"+`data: {"response":`); err != nil {
		return err
	}
	if err := writeCompletedResponse(writer, ids, model, b64Raw, revisedRaw, usageRaw); err != nil {
		return err
	}
	if err := writeString(writer, `,"type":"response.completed","sequence_number":8}`+"\n\n"); err != nil {
		return err
	}
	return writeString(writer, "data: [DONE]\n\n")
}

func writeJSONString(writer io.Writer, value string) error {
	raw, err := common.Marshal(value)
	if err != nil {
		return err
	}
	_, err = writer.Write(raw)
	return err
}

func writeString(writer io.Writer, value string) error {
	_, err := io.WriteString(writer, value)
	return err
}
