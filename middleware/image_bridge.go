package middleware

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/imagebridge"
	"github.com/QuantumNous/new-api/relaykit/types"
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
			if common.IsRequestBodyStalledError(err) {
				// Half-open client upload: fail the request fast with 408
				// instead of holding the worker until the client's own
				// timeout closes the connection.
				abortWithOpenAiMessage(c, http.StatusRequestTimeout, fmt.Sprintf("request body stalled: %v", err), types.ErrorCodeRequestBodyStalled)
				return
			}
			abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", err))
			return
		}
		var intent imagebridge.Intent
		var matched bool
		var raw []byte
		if inspectMultipart {
			intent, matched, err = imagebridge.DetectMultipart(c.Request.URL.Path, storage, c.GetHeader("Content-Type"))
			if err != nil {
				abortWithOpenAiMessage(c, http.StatusBadRequest, err.Error())
				return
			}
		} else {
			var hasImageIntent bool
			if storage.IsDisk() {
				reader, openErr := storage.NewReader()
				if openErr != nil {
					abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", openErr))
					return
				}
				hasImageIntent = imagebridge.MayContainImageIntentStreaming(reader)
				_ = reader.Close()
			} else {
				var readErr error
				raw, readErr = storage.Bytes()
				if readErr != nil {
					abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", readErr))
					return
				}
				hasImageIntent = imagebridge.MayContainImageIntent(raw)
			}

			// Fast-path bypass: for /v1/chat/completions and /v1/responses, if the
			// body does not contain any image model prefixes, image tool names, or
			// image modality keywords, it cannot be an image request and does not
			// need AST parsing.
			if isImagesPath || hasImageIntent {
				reader, openErr := storage.NewReader()
				if openErr != nil {
					abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", openErr))
					return
				}
				intent, matched, err = imagebridge.DetectJSONReader(c.Request.URL.Path, reader)
				_ = reader.Close()
				if err != nil {
					abortWithOpenAiMessage(c, http.StatusBadRequest, err.Error())
					return
				}
			}
		}

		if matched {
			if !intent.StreamSet && intent.Envelope != imagebridge.EnvelopeImages &&
				strings.Contains(strings.ToLower(c.GetHeader("Accept")), "text/event-stream") {
				intent.Stream = true
				intent.Request.Stream = common.GetPointer(true)
			}
			imagebridge.SetContext(c, intent)
		} else if !inspectMultipart && (path == "/v1/responses" || path == "/v1/chat/completions") {
			// Mixed GPT requests stay on CPA, but the provider-native image tool
			// must be private so CPA can plan without executing GPT image output.
			// Applies to all managed key scopes, unrestricted, and subscription scopes.
			// Operates identically on memory and disk-backed storage without loading large bodies into RAM.
			envelope := imagebridge.EnvelopeChat
			if path == "/v1/responses" {
				envelope = imagebridge.EnvelopeResponses
			}
			rewrittenStorage, changed, rewriteErr := imagebridge.DetectAndRewriteJSONStorage(storage, envelope)
			if rewriteErr != nil {
				abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image planner request: %v", rewriteErr))
				return
			}
			if changed {
				c.Set(common.KeyBodyStorage, rewrittenStorage)
				c.Request.ContentLength = rewrittenStorage.Size()
				storage = rewrittenStorage
			}
		}
		if _, err := storage.Seek(0, io.SeekStart); err != nil {
			abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", err))
			return
		}
		c.Request.Body = io.NopCloser(storage)
		c.Next()
	}
}
