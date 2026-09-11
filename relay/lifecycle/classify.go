package lifecycle

import (
	"bytes"
	"strings"
	"unicode"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/dto"

	"github.com/gin-gonic/gin"
)

var pingLiteral = []byte(": PING")

func ClassifyDownstreamPayload(c *gin.Context, data []byte) {
	lr := FromContext(c)
	classifyWrite(lr, data)
}

// ShouldHoldDownstream reports that this payload must not reach the customer
// yet: the logical request is still pre-semantic and the bytes are not a
// keepalive comment.
func ShouldHoldDownstream(c *gin.Context, data []byte) bool {
	lr := FromContext(c)
	if lr == nil || lr.SemanticCommitted() {
		return false
	}
	trimmed := bytes.TrimSpace(data)
	if isKeepaliveSSE(trimmed) || isSSEComment(trimmed) {
		return false
	}
	lr.Hold().Push(string(data))
	return true
}

func classifyWrite(lr *LogicalRequest, data []byte) {
	if lr == nil || len(data) == 0 {
		return
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		lr.MarkHeadersCommitted()
		return
	}
	if isKeepaliveSSE(trimmed) {
		lr.MarkKeepaliveOnly()
		return
	}
	if isSSEComment(trimmed) {
		lr.MarkKeepaliveOnly()
		return
	}
	if classifySemanticPayload(trimmed) {
		lr.MarkSemanticCommitted()
		return
	}
	// Hold-buffer events such as response.created are JSON but not semantic.
	lr.MarkHeadersCommitted()
}

func isKeepaliveSSE(b []byte) bool {
	s := strings.TrimSpace(string(b))
	if s == ": PING" || strings.HasPrefix(s, ": PING\n") {
		return true
	}
	return bytes.Equal(bytes.TrimSpace(b), pingLiteral) || bytes.HasPrefix(bytes.TrimSpace(b), append(pingLiteral, '\n'))
}

func isSSEComment(b []byte) bool {
	s := strings.TrimLeftFunc(string(b), unicode.IsSpace)
	if s == "" {
		return false
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, ":") {
			return false
		}
	}
	return true
}

func looksLikeJSON(b []byte) bool {
	s := bytes.TrimSpace(b)
	return len(s) > 0 && (s[0] == '{' || s[0] == '[')
}

func classifySemanticPayload(b []byte) bool {
	payload := extractSSEData(b)
	if payload == "" {
		payload = strings.TrimSpace(string(b))
	}
	if payload == "" || payload == "[DONE]" {
		return false
	}
	if strings.Contains(payload, `"delta"`) || strings.Contains(payload, `"content"`) {
		var stream dto.ChatCompletionsStreamResponse
		if err := common.Unmarshal([]byte(payload), &stream); err == nil {
			if stream.Id != "" {
				// response.created / id-only chunks are held until real semantic
				// content exists. A bound id plus empty choices is not semantic.
				if streamHasSemantic(&stream) {
					return true
				}
			} else if streamHasSemantic(&stream) {
				return true
			}
		}
	}
	var resp dto.ResponsesStreamResponse
	if err := common.Unmarshal([]byte(payload), &resp); err == nil && resp.Type != "" {
		switch resp.Type {
		case "response.created", "response.in_progress":
			return false
		case "response.output_text.delta", "response.output_item.added", "response.output_item.done",
			"response.function_call_arguments.delta", "response.function_call_arguments.done",
			"response.completed", "response.failed", "response.incomplete":
			return resp.Type != "response.created"
		default:
			if strings.Contains(resp.Type, "delta") || strings.Contains(resp.Type, "output") ||
				strings.Contains(resp.Type, "image") || strings.Contains(resp.Type, "audio") ||
				strings.Contains(resp.Type, "function") || strings.Contains(resp.Type, "tool") {
				return true
			}
			if resp.Type == "error" || resp.Type == "response.failed" {
				return true
			}
		}
	}
	if strings.Contains(payload, `"tool_calls"`) || strings.Contains(payload, `"function_call"`) {
		return true
	}
	if strings.Contains(payload, `"b64_json"`) || strings.Contains(payload, `"image_url"`) ||
		strings.Contains(payload, `"audio"`) {
		return true
	}
	if strings.Contains(payload, `"choices"`) && strings.Contains(payload, `"content"`) {
		return true
	}
	return false
}

func streamHasSemantic(stream *dto.ChatCompletionsStreamResponse) bool {
	if stream == nil {
		return false
	}
	for _, ch := range stream.Choices {
		if ch.Delta.GetContentString() != "" || ch.Delta.GetReasoningContent() != "" {
			return true
		}
		if len(ch.Delta.ToolCalls) > 0 {
			return true
		}
		if ch.FinishReason != nil && *ch.FinishReason != "" {
			return true
		}
	}
	return false
}

func extractSSEData(b []byte) string {
	var chunks []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "data:") {
			chunks = append(chunks, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	return strings.Join(chunks, "\n")
}

// ShouldHoldUpstreamEvent reports whether a stream event from a failed attempt
// must be suppressed until semantic commit. response.created / id / usage
// without content are held.
func ShouldHoldUpstreamEvent(data string) bool {
	payload := strings.TrimSpace(data)
	if payload == "" || payload == "[DONE]" {
		return true
	}
	var stream dto.ChatCompletionsStreamResponse
	if err := common.Unmarshal([]byte(payload), &stream); err == nil {
		if stream.Id != "" && !streamHasSemantic(&stream) {
			return true
		}
		if stream.Usage != nil && !streamHasSemantic(&stream) {
			return true
		}
	}
	var resp dto.ResponsesStreamResponse
	if err := common.Unmarshal([]byte(payload), &resp); err == nil && resp.Type != "" {
		switch resp.Type {
		case "response.created", "response.in_progress":
			return true
		}
	}
	return false
}

const SessionStateInvalidMessage = "会话状态无法验证或已失效，请新建会话后重试"

func PreserveSessionStateError(code, raw string) (keepCode string, message string, stripReplay bool) {
	c := strings.ToLower(strings.TrimSpace(code))
	body := strings.ToLower(raw)
	if strings.Contains(c, "thinking_signature_invalid") || strings.Contains(body, "thinking_signature_invalid") ||
		strings.Contains(c, "encrypted_content") || strings.Contains(body, "encrypted_content") {
		return c, SessionStateInvalidMessage, false
	}
	return code, raw, false
}
