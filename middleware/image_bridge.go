package middleware

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/imagebridge"
	"github.com/gin-gonic/gin"
)

// DetectImageBridge identifies public OpenAI-compatible image requests before
// channel distribution. It stores only the compact routing intent; the
// request-owned BodyStorage remains the source of truth for input images.
func DetectImageBridge() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request == nil || c.Request.Method != http.MethodPost {
			c.Next()
			return
		}
		contentType := strings.ToLower(strings.TrimSpace(c.GetHeader("Content-Type")))
		path := strings.TrimSuffix(strings.SplitN(c.Request.URL.Path, "?", 2)[0], "/")
		isImagesPath := path == "/v1/images" || strings.HasPrefix(path, "/v1/images/")
		isJSON := contentType == "" || strings.HasPrefix(contentType, "application/json")
		isMultipart := strings.HasPrefix(contentType, "multipart/form-data")
		inspectJSON := isJSON && (isImagesPath || path == "/v1/responses" || path == "/v1/chat/completions")
		inspectMultipart := isMultipart && isImagesPath
		if !inspectJSON && !inspectMultipart {
			c.Next()
			return
		}

		storage, err := common.GetBodyStorage(c)
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", err))
			return
		}
		var intent imagebridge.Intent
		var matched bool
		if inspectMultipart {
			intent, matched, err = imagebridge.DetectMultipart(c.Request.URL.Path, storage, c.GetHeader("Content-Type"))
		} else {
			intent, matched, err = imagebridge.DetectJSONReader(c.Request.URL.Path, storage)
		}
		if err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, err.Error())
			return
		}
		if matched {
			if !intent.StreamSet && intent.Envelope != imagebridge.EnvelopeImages &&
				strings.Contains(strings.ToLower(c.GetHeader("Accept")), "text/event-stream") {
				intent.Stream = true
				intent.Request.Stream = common.GetPointer(true)
			}
			imagebridge.SetContext(c, intent)
		}
		if _, err := storage.Seek(0, io.SeekStart); err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", err))
			return
		}
		c.Request.Body = io.NopCloser(storage)
		c.Next()
	}
}
