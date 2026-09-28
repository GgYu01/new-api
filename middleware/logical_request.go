package middleware

import (
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/lifecycle"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// LogicalRequest installs a request-owned G2 lifecycle before distributor so
// root identity, writer arbiter, and heartbeat span channel selection. It does
// not auto-retry distributor "no available channel" 503s.
func LogicalRequest() gin.HandlerFunc {
	return func(c *gin.Context) {
		if lifecycle.FromContext(c) != nil {
			c.Next()
			return
		}
		rootID := c.GetString(common.RequestIdKey)
		if rootID == "" {
			rootID = common.NewRequestId()
			c.Set(common.RequestIdKey, rootID)
		}
		stream := wantsStream(c)
		format := formatFromPath(c.Request.URL.Path)
		lr := lifecycle.New(c.Request.Context(), rootID, stream, format)
		lifecycle.Install(c, lr)
		lr.AttachWriter(c)
		defer lr.Cleanup()
		c.Next()
	}
}

func wantsStream(c *gin.Context) bool {
	if common.GetContextKeyBool(c, constant.ContextKeyIsStream) {
		return true
	}
	accept := strings.ToLower(c.GetHeader("Accept"))
	if strings.Contains(accept, "text/event-stream") {
		return true
	}
	return false
}

func formatFromPath(path string) types.RelayFormat {
	switch {
	case strings.Contains(path, "/messages"):
		return types.RelayFormatClaude
	case strings.Contains(path, "/responses"):
		return types.RelayFormatOpenAIResponses
	case strings.Contains(path, "/embeddings"):
		return types.RelayFormatEmbedding
	case strings.Contains(path, "/images"):
		return types.RelayFormatOpenAIImage
	case strings.Contains(path, "/audio"):
		return types.RelayFormatOpenAIAudio
	case strings.Contains(path, "/realtime"):
		return types.RelayFormatOpenAIRealtime
	default:
		return types.RelayFormatOpenAI
	}
}
