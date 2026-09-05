package middleware

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/imagebridge"
	"github.com/gin-gonic/gin"
)

// containsFoldASCII checks whether b contains substr case-insensitively for ASCII characters.
func containsFoldASCII(b []byte, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(b) < len(sub) {
		return false
	}
	firstLower := byte(sub[0])
	if firstLower >= 'A' && firstLower <= 'Z' {
		firstLower += 'a' - 'A'
	}
	firstUpper := firstLower
	if firstLower >= 'a' && firstLower <= 'z' {
		firstUpper -= 'a' - 'A'
	}
	subLen := len(sub)
	maxI := len(b) - subLen
	for i := 0; i <= maxI; i++ {
		c := b[i]
		if c == firstLower || c == firstUpper {
			match := true
			for j := 1; j < subLen; j++ {
				cb := b[i+j]
				if cb >= 'A' && cb <= 'Z' {
					cb += 'a' - 'A'
				}
				sb := byte(sub[j])
				if sb >= 'A' && sb <= 'Z' {
					sb += 'a' - 'A'
				}
				if cb != sb {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}

func mayContainImageIntent(raw []byte) bool {
	return containsFoldASCII(raw, "image") ||
		containsFoldASCII(raw, "dall") ||
		containsFoldASCII(raw, "banana")
}

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
		var raw []byte
		if inspectMultipart {
			intent, matched, err = imagebridge.DetectMultipart(c.Request.URL.Path, storage, c.GetHeader("Content-Type"))
			if err != nil {
				abortWithOpenAiMessage(c, http.StatusBadRequest, err.Error())
				return
			}
		} else {
			var readErr error
			raw, readErr = storage.Bytes()
			if readErr != nil {
				abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", readErr))
				return
			}

			// Fast-path bypass: for /v1/chat/completions and /v1/responses, if the
			// body does not contain any image model prefixes, image tool names, or
			// image modality keywords, it cannot be an image request and does not
			// need AST parsing.
			if isImagesPath || mayContainImageIntent(raw) {
				intent, matched, err = imagebridge.DetectJSONReader(c.Request.URL.Path, storage)
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
		} else if !inspectMultipart && common.GetContextKeyString(c, constant.ContextKeyTokenSubscriptionType) == "gptopenaicodex" && (path == "/v1/responses" || path == "/v1/chat/completions") {
			// Mixed GPT requests stay on CPA, but the provider-native image tool
			// must be private so CPA can plan without executing GPT image output.
			if bytes.Contains(raw, []byte(`"tools"`)) && containsFoldASCII(raw, "image") {
				envelope := imagebridge.EnvelopeChat
				if path == "/v1/responses" {
					envelope = imagebridge.EnvelopeResponses
				}
				if rewritten, changed, rewriteErr := imagebridge.RewriteAutoImageToolForEnvelope(raw, envelope); rewriteErr != nil {
					abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image planner request: %v", rewriteErr))
					return
				} else if changed {
					// One-shot ownership transfer: publish the new storage in the
					// context and request first, then release the old owner.
					newStorage, createErr := common.CreateBodyStorage(rewritten)
					if createErr != nil {
						abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image planner request: %v", createErr))
						return
					}
					c.Set(common.KeyBodyStorage, newStorage)
					c.Request.ContentLength = int64(len(rewritten))
					_ = storage.Close()
					storage = newStorage
				}
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
