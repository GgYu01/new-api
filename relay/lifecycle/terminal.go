package lifecycle

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/imagebridge"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// WriteFailure writes a format-specific SSE terminal after headers are committed.
// It never rewrites the HTTP status.
func WriteFailure(c *gin.Context, lr *LogicalRequest, apiErr *types.NewAPIError) error {
	if c == nil || apiErr == nil {
		return nil
	}
	if lr != nil && lr.TerminalSent() {
		return nil
	}
	msg := apiErr.Error()
	if requestID := c.GetString(common.RequestIdKey); requestID != "" {
		msg = common.MessageWithRequestId(msg, requestID)
	}
	format := types.RelayFormatOpenAI
	if lr != nil {
		format = lr.format
	}

	if intent, ok := imagebridge.FromContext(c); ok && intent.Stream {
		ids := imagebridge.NewIDs(c.GetString(common.RequestIdKey))
		err := imagebridge.WriteFailure(c.Writer, *intent, ids, msg)
		if flusher, ok := c.Writer.(http.Flusher); ok {
			flusher.Flush()
		}
		if lr != nil {
			lr.MarkTerminalSent()
		}
		return err
	}

	payload := failureSSE(format, msg, apiErr)
	if lr != nil && lr.writer != nil {
		_, err := lr.writer.WriteTerminal(payload)
		return err
	}
	_, err := c.Writer.Write(payload)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	if lr != nil {
		lr.MarkTerminalSent()
	}
	return err
}

func failureSSE(format types.RelayFormat, msg string, apiErr *types.NewAPIError) []byte {
	code := string(apiErr.GetErrorCode())
	if code == "" {
		code = "upstream_error"
	}
	switch format {
	case types.RelayFormatClaude:
		body, _ := common.Marshal(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    "api_error",
				"message": msg,
			},
		})
		return []byte(fmt.Sprintf("event: error\ndata: %s\n\n", body))
	case types.RelayFormatOpenAIResponses, types.RelayFormatGemini:
		body, _ := common.Marshal(map[string]any{
			"type": "response.failed",
			"response": map[string]any{
				"status": "failed",
				"error": map[string]any{
					"code":    code,
					"message": msg,
				},
			},
		})
		return []byte(fmt.Sprintf("event: response.failed\ndata: %s\n\ndata: [DONE]\n\n", body))
	default:
		body, _ := common.Marshal(map[string]any{
			"error": map[string]any{
				"message": msg,
				"type":    "server_error",
				"code":    code,
			},
		})
		return []byte(fmt.Sprintf("data: %s\n\ndata: [DONE]\n\n", body))
	}
}
