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

func decodeHexDigit(b byte) (int, bool) {
	switch {
	case b >= '0' && b <= '9':
		return int(b - '0'), true
	case b >= 'a' && b <= 'f':
		return int(b - 'a' + 10), true
	case b >= 'A' && b <= 'F':
		return int(b - 'A' + 10), true
	default:
		return 0, false
	}
}

// containsJSONKeyword checks whether b contains keyword case-insensitively,
// correctly matching both plain ASCII and RFC 8259 \u00XX hex escapes (e.g. \u0069mage, \u0074ools).
func containsJSONKeyword(b []byte, keyword string) bool {
	if len(keyword) == 0 {
		return true
	}
	n := len(b)
	kLen := len(keyword)
	for i := 0; i < n; i++ {
		curr := i
		matched := true
		for k := 0; k < kLen; k++ {
			if curr >= n {
				matched = false
				break
			}
			c := b[curr]
			target := keyword[k]
			targetLower := target
			if targetLower >= 'A' && targetLower <= 'Z' {
				targetLower += 'a' - 'A'
			}

			cLower := c
			if cLower >= 'A' && cLower <= 'Z' {
				cLower += 'a' - 'A'
			}
			if cLower == targetLower {
				curr++
				continue
			}

			// Check JSON unicode escape \u00XX
			if c == '\\' && curr+5 < n && (b[curr+1] == 'u' || b[curr+1] == 'U') {
				d1, ok1 := decodeHexDigit(b[curr+2])
				d2, ok2 := decodeHexDigit(b[curr+3])
				d3, ok3 := decodeHexDigit(b[curr+4])
				d4, ok4 := decodeHexDigit(b[curr+5])
				if ok1 && ok2 && ok3 && ok4 {
					val := (d1 << 12) | (d2 << 8) | (d3 << 4) | d4
					if val < 256 {
						valByte := byte(val)
						if valByte >= 'A' && valByte <= 'Z' {
							valByte += 'a' - 'A'
						}
						if valByte == targetLower {
							curr += 6
							continue
						}
					}
				}
			}

			// Check escaped slash \/
			if c == '\\' && curr+1 < n && b[curr+1] == '/' && target == '/' {
				curr += 2
				continue
			}

			matched = false
			break
		}
		if matched {
			return true
		}
	}
	return false
}

func mayContainImageIntent(raw []byte) bool {
	return containsJSONKeyword(raw, "image") ||
		containsJSONKeyword(raw, "dall") ||
		containsJSONKeyword(raw, "banana")
}

// mayContainImageIntentStreaming scans a reader in 64KB chunks with an overlap
// window to detect image keywords without allocating a contiguous full-file buffer.
func mayContainImageIntentStreaming(r io.Reader) bool {
	const chunkSize = 64 << 10
	const overlap = 64
	buf := make([]byte, chunkSize+overlap)
	carry := 0
	for {
		n, err := io.ReadFull(r, buf[carry:])
		readTotal := carry + n
		if readTotal == 0 {
			break
		}
		segment := buf[:readTotal]
		if mayContainImageIntent(segment) {
			return true
		}
		if err != nil {
			break
		}
		copy(buf[:overlap], buf[readTotal-overlap:readTotal])
		carry = overlap
	}
	return false
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
			var hasImageIntent bool
			if storage.IsDisk() {
				reader, openErr := storage.NewReader()
				if openErr != nil {
					abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", openErr))
					return
				}
				hasImageIntent = mayContainImageIntentStreaming(reader)
				_ = reader.Close()
			} else {
				var readErr error
				raw, readErr = storage.Bytes()
				if readErr != nil {
					abortWithOpenAiMessage(c, http.StatusBadRequest, fmt.Sprintf("invalid image bridge request: %v", readErr))
					return
				}
				hasImageIntent = mayContainImageIntent(raw)
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
			if len(raw) == 0 && !storage.IsDisk() {
				raw, _ = storage.Bytes()
			}
			if len(raw) > 0 && containsJSONKeyword(raw, "tools") && containsJSONKeyword(raw, "image") {
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
