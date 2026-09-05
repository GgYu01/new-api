package imagebridge

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxJSONDepth          = 64
	maxJSONNodes          = 100_000
	maxJSONStringPreview  = 32 << 10
	maxJSONPreviewBytes   = 2 << 20
	maxJSONObjectKeyBytes = 4 << 10
)

type scannedString struct {
	value     string
	rawOffset int64
	rawLength int64
	truncated bool
}

type imageSourceKind uint8

const (
	imageSourceDataURL imageSourceKind = iota + 1
	imageSourceRawBase64
	imageSourceURL
)

// InputImage describes a request image without retaining its base64 payload.
// rawOffset/rawLength point into the request-owned BodyStorage and are used by
// the outbound multipart producer only while the request is alive.
type InputImage struct {
	Filename  string
	MIME      string
	URL       string
	kind      imageSourceKind
	rawOffset int64
	rawLength int64
	preview   string
}

func DetectJSONReader(path string, reader io.Reader) (Intent, bool, error) {
	parser := newJSONStreamParser(reader)
	value, err := parser.parseDocument()
	if err != nil {
		return Intent{}, false, fmt.Errorf("invalid image bridge JSON: %w", err)
	}
	payload, ok := value.(map[string]any)
	if !ok {
		return Intent{}, false, fmt.Errorf("invalid image bridge JSON: top-level value must be an object")
	}

	intent, matched, err := detect(path, payload)
	if err != nil || !matched {
		return intent, matched, err
	}
	intent.Source = SourceJSON
	intent.InputImages = collectJSONInputImages(payload)
	if len(intent.InputImages) > 0 {
		intent.ImageCount = len(intent.InputImages)
		intent.Mode = ModeEdit
		if strings.TrimSpace(intent.Request.Prompt) == "" {
			if normalizedPath(path) == "/v1/images/variations" {
				intent.Request.Prompt = DefaultVariationPrompt
			} else {
				intent.Request.Prompt = defaultEditPrompt
			}
		}
	}
	if intent.Mode == ModeEdit && len(intent.InputImages) == 0 {
		return Intent{}, false, fmt.Errorf("image file or image_url is required")
	}
	return intent, true, nil
}

func normalizedPath(path string) string {
	return strings.TrimSuffix(strings.SplitN(path, "?", 2)[0], "/")
}

type jsonStreamParser struct {
	reader           *bufio.Reader
	offset           int64
	nodes            int
	previewRemaining int
}

func newJSONStreamParser(reader io.Reader) *jsonStreamParser {
	return &jsonStreamParser{
		reader:           bufio.NewReaderSize(reader, 32<<10),
		previewRemaining: maxJSONPreviewBytes,
	}
}

func (p *jsonStreamParser) parseDocument() (any, error) {
	if err := p.skipSpace(); err != nil {
		return nil, err
	}
	value, err := p.parseValue(0)
	if err != nil {
		return nil, err
	}
	if err := p.skipSpace(); err != nil {
		if err == io.EOF {
			return value, nil
		}
		return nil, err
	}
	if _, err := p.readByte(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("unexpected data after top-level value")
		}
		return nil, err
	}
	return value, nil
}

func (p *jsonStreamParser) parseValue(depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, fmt.Errorf("JSON nesting exceeds %d", maxJSONDepth)
	}
	p.nodes++
	if p.nodes > maxJSONNodes {
		return nil, fmt.Errorf("JSON value count exceeds %d", maxJSONNodes)
	}
	if err := p.skipSpace(); err != nil {
		return nil, err
	}
	b, err := p.readByte()
	if err != nil {
		return nil, err
	}
	switch b {
	case '{':
		return p.parseObject(depth + 1)
	case '[':
		return p.parseArray(depth + 1)
	case '"':
		return p.parseString()
	default:
		return p.parseLiteral(b)
	}
}

func (p *jsonStreamParser) parseObject(depth int) (map[string]any, error) {
	object := make(map[string]any)
	if err := p.skipSpace(); err != nil {
		return nil, err
	}
	b, err := p.readByte()
	if err != nil {
		return nil, err
	}
	if b == '}' {
		return object, nil
	}
	if err := p.unreadByte(); err != nil {
		return nil, err
	}
	for {
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		quote, err := p.readByte()
		if err != nil {
			return nil, err
		}
		if quote != '"' {
			return nil, fmt.Errorf("object key must be a string at byte %d", p.offset)
		}
		keyToken, err := p.parseObjectKey()
		if err != nil {
			return nil, err
		}
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		colon, err := p.readByte()
		if err != nil {
			return nil, err
		}
		if colon != ':' {
			return nil, fmt.Errorf("missing colon after object key %q", keyToken.value)
		}
		value, err := p.parseValue(depth)
		if err != nil {
			return nil, err
		}
		object[keyToken.value] = value
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		separator, err := p.readByte()
		if err != nil {
			return nil, err
		}
		switch separator {
		case '}':
			return object, nil
		case ',':
			continue
		default:
			return nil, fmt.Errorf("invalid object separator at byte %d", p.offset)
		}
	}
}

// parseObjectKey parses a key independently of the value preview budget. A
// large request value must not make a later, ordinary key look truncated.
func (p *jsonStreamParser) parseObjectKey() (scannedString, error) {
	start := p.offset
	raw := make([]byte, 0, min(maxJSONObjectKeyBytes, 256))
	escaped := false
	for {
		b, err := p.readByte()
		if err != nil {
			return scannedString{}, fmt.Errorf("unterminated JSON string: %w", err)
		}
		if !escaped && b == '"' {
			break
		}
		if !escaped && b < 0x20 {
			return scannedString{}, fmt.Errorf("unescaped control character in JSON string")
		}
		if len(raw) < maxJSONObjectKeyBytes {
			raw = append(raw, b)
		} else {
			return scannedString{}, fmt.Errorf("object key exceeds %d bytes", maxJSONObjectKeyBytes)
		}
		if escaped {
			escaped = false
		} else if b == '\\' {
			escaped = true
		}
	}
	rawLength := p.offset - start - 1
	value, err := jsonUnquote(raw)
	if err != nil {
		return scannedString{}, fmt.Errorf("invalid JSON string: %w", err)
	}
	return scannedString{value: value, rawOffset: start, rawLength: rawLength}, nil
}

func (p *jsonStreamParser) parseArray(depth int) ([]any, error) {
	array := make([]any, 0)
	if err := p.skipSpace(); err != nil {
		return nil, err
	}
	b, err := p.readByte()
	if err != nil {
		return nil, err
	}
	if b == ']' {
		return array, nil
	}
	if err := p.unreadByte(); err != nil {
		return nil, err
	}
	for {
		value, err := p.parseValue(depth)
		if err != nil {
			return nil, err
		}
		array = append(array, value)
		if err := p.skipSpace(); err != nil {
			return nil, err
		}
		separator, err := p.readByte()
		if err != nil {
			return nil, err
		}
		switch separator {
		case ']':
			return array, nil
		case ',':
			continue
		default:
			return nil, fmt.Errorf("invalid array separator at byte %d", p.offset)
		}
	}
}

func (p *jsonStreamParser) parseString() (scannedString, error) {
	start := p.offset
	limit := maxJSONStringPreview
	if p.previewRemaining < limit {
		limit = p.previewRemaining
	}
	rawPreview := make([]byte, 0, min(limit, 256))
	escaped := false
	for {
		b, err := p.readByte()
		if err != nil {
			return scannedString{}, fmt.Errorf("unterminated JSON string: %w", err)
		}
		if !escaped && b == '"' {
			break
		}
		if !escaped && b < 0x20 {
			return scannedString{}, fmt.Errorf("unescaped control character in JSON string")
		}
		if len(rawPreview) < limit {
			rawPreview = append(rawPreview, b)
		}
		if escaped {
			escaped = false
		} else if b == '\\' {
			escaped = true
		}
	}
	rawLength := p.offset - start - 1
	truncated := rawLength > int64(len(rawPreview))
	p.previewRemaining -= len(rawPreview)
	value, err := decodeJSONStringPreview(rawPreview, truncated)
	if err != nil {
		return scannedString{}, err
	}
	return scannedString{value: value, rawOffset: start, rawLength: rawLength, truncated: truncated}, nil
}

func decodeJSONStringPreview(raw []byte, truncated bool) (string, error) {
	for trim := 0; trim <= 8 && trim <= len(raw); trim++ {
		candidate := raw[:len(raw)-trim]
		value, err := jsonUnquote(candidate)
		if err == nil {
			return value, nil
		}
		if !truncated {
			return "", fmt.Errorf("invalid JSON string: %w", err)
		}
	}
	if truncated {
		return string(raw), nil
	}
	return "", fmt.Errorf("invalid JSON string")
}

// jsonUnquote decodes a JSON string token's raw bytes (quotes excluded) with
// JSON string semantics. strconv.Unquote is not usable here: it implements Go
// string-literal semantics, which reject legal JSON escapes such as \/ and
// non-BMP surrogate pairs while accepting Go-only escapes (\xNN, \a, \v,
// octal) that are invalid JSON. Behavior mirrors encoding/json exactly:
// lone or partial surrogates decode to U+FFFD, invalid UTF-8 is replaced with
// U+FFFD, and every non-JSON escape is an error. The no-escape fast path
// avoids copying twice for the dominant unescaped case.
func jsonUnquote(raw []byte) (string, error) {
	if !bytes.ContainsRune(raw, '\\') {
		if bytes.IndexByte(raw, '"') >= 0 {
			return "", fmt.Errorf("unescaped quote in JSON string")
		}
		if utf8.Valid(raw) {
			return string(raw), nil
		}
		return strings.ToValidUTF8(string(raw), string(utf8.RuneError)), nil
	}
	return jsonUnescape(raw)
}

func jsonUnescape(raw []byte) (string, error) {
	decoded := make([]byte, 0, len(raw))
	for i := 0; i < len(raw); {
		c := raw[i]
		if c != '\\' {
			if c == '"' {
				return "", fmt.Errorf("unescaped quote in JSON string")
			}
			if c < 0x20 {
				return "", fmt.Errorf("unescaped control character in JSON string")
			}
			if c < utf8.RuneSelf {
				decoded = append(decoded, c)
				i++
				continue
			}
			r, size := utf8.DecodeRune(raw[i:])
			// DecodeRune returns (RuneError, 1) for invalid bytes; AppendRune
			// then emits U+FFFD, matching encoding/json.
			decoded = utf8.AppendRune(decoded, r)
			i += size
			continue
		}
		if i+1 >= len(raw) {
			return "", fmt.Errorf("trailing escape in JSON string")
		}
		switch e := raw[i+1]; e {
		case '"', '\\', '/':
			decoded = append(decoded, e)
			i += 2
		case 'b':
			decoded = append(decoded, '\b')
			i += 2
		case 'f':
			decoded = append(decoded, '\f')
			i += 2
		case 'n':
			decoded = append(decoded, '\n')
			i += 2
		case 'r':
			decoded = append(decoded, '\r')
			i += 2
		case 't':
			decoded = append(decoded, '\t')
			i += 2
		case 'u':
			r, consumed, ok := jsonUnescapeUnicodeUnit(raw[i:])
			if !ok {
				return "", fmt.Errorf("invalid \\u escape in JSON string")
			}
			if utf16.IsSurrogate(rune(r)) {
				if r2, consumed2, ok2 := jsonUnescapeUnicodeUnit(raw[i+consumed:]); ok2 && utf16.IsSurrogate(rune(r2)) {
					if combined := utf16.DecodeRune(rune(r), rune(r2)); combined != unicode.ReplacementChar {
						decoded = utf8.AppendRune(decoded, combined)
						i += consumed + consumed2
						continue
					}
				}
				r = utf8.RuneError // lone surrogate decodes to U+FFFD
			}
			decoded = utf8.AppendRune(decoded, r)
			i += consumed
		default:
			return "", fmt.Errorf("invalid escape \\%c in JSON string", e)
		}
	}
	return string(decoded), nil
}

// jsonUnescapeUnicodeUnit decodes one leading \uXXXX escape, returning the raw
// unit value and the consumed byte count. It requires exactly 4 hex digits,
// like encoding/json's getu4.
func jsonUnescapeUnicodeUnit(raw []byte) (rune, int, bool) {
	if len(raw) < 6 || raw[0] != '\\' || raw[1] != 'u' {
		return 0, 0, false
	}
	value := 0
	for _, d := range raw[2:6] {
		switch {
		case d >= '0' && d <= '9':
			value = value<<4 | int(d-'0')
		case d >= 'a' && d <= 'f':
			value = value<<4 | int(d-'a'+10)
		case d >= 'A' && d <= 'F':
			value = value<<4 | int(d-'A'+10)
		default:
			return 0, 0, false
		}
	}
	return rune(value), 6, true
}

func (p *jsonStreamParser) parseLiteral(first byte) (any, error) {
	token := []byte{first}
	for len(token) <= 256 {
		b, err := p.readByte()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if isJSONDelimiter(b) {
			if err := p.unreadByte(); err != nil {
				return nil, err
			}
			break
		}
		token = append(token, b)
	}
	if len(token) > 256 {
		return nil, fmt.Errorf("JSON literal is too long")
	}
	var value any
	if err := json.Unmarshal(token, &value); err != nil {
		return nil, fmt.Errorf("invalid JSON literal %q: %w", token, err)
	}
	return value, nil
}

func isJSONDelimiter(b byte) bool {
	return b == ',' || b == '}' || b == ']' || b == ' ' || b == '\t' || b == '\r' || b == '\n'
}

func (p *jsonStreamParser) skipSpace() error {
	for {
		b, err := p.readByte()
		if err != nil {
			return err
		}
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return p.unreadByte()
		}
	}
}

func (p *jsonStreamParser) readByte() (byte, error) {
	b, err := p.reader.ReadByte()
	if err == nil {
		p.offset++
	}
	return b, err
}

func (p *jsonStreamParser) unreadByte() error {
	if err := p.reader.UnreadByte(); err != nil {
		return err
	}
	p.offset--
	return nil
}

func collectJSONInputImages(payload map[string]any) []InputImage {
	images := make([]InputImage, 0, 4)
	for _, key := range []string{
		"image", "images", "image[]", "images[]", "image_url", "image_urls",
		"file", "files", "image1", "image2", "image3", "image4", "img", "img1", "img2",
		"reference", "references", "input_image", "input_images", "image_base64", "images_base64",
	} {
		if value, exists := payload[key]; exists {
			appendInputImageRefs(&images, value, true, "", "")
		}
	}
	walkJSONInputImages(payload["input"], &images, 0)
	walkJSONInputImages(payload["messages"], &images, 0)
	return dedupeInputImages(images, 4)
}

func walkJSONInputImages(value any, images *[]InputImage, depth int) {
	if depth > 8 || len(*images) >= 8 || value == nil {
		return
	}
	switch node := value.(type) {
	case []any:
		for _, item := range node {
			walkJSONInputImages(item, images, depth+1)
		}
	case map[string]any:
		typeName := strings.ToLower(stringValue(node["type"]))
		if typeName == "input_image" || typeName == "image_url" || typeName == "image" {
			mimeHint := stringValue(node["mime_type"])
			if mimeHint == "" {
				mimeHint = stringValue(node["mimeType"])
			}
			filename := stringValue(node["filename"])
			for _, key := range []string{"b64_json", "base64", "data", "image_url", "url", "image"} {
				if child, exists := node[key]; exists {
					before := len(*images)
					appendInputImageRefs(images, child, true, mimeHint, filename)
					if len(*images) > before {
						return
					}
				}
			}
		}
		for _, key := range []string{"content", "input", "parts", "message"} {
			walkJSONInputImages(node[key], images, depth+1)
		}
	}
}

func appendInputImageRefs(images *[]InputImage, value any, allowRaw bool, mimeHint, filename string) {
	if len(*images) >= 8 || value == nil {
		return
	}
	switch node := value.(type) {
	case scannedString:
		if ref, ok := inputImageFromString(node, allowRaw, mimeHint, filename); ok {
			*images = append(*images, ref)
		}
	case string:
		// Regular strings are retained only for compatibility with callers that
		// construct a payload map directly. They have no storage offsets, so only
		// URL references are usable by the streaming outbound path.
		if strings.HasPrefix(strings.TrimSpace(node), "http://") || strings.HasPrefix(strings.TrimSpace(node), "https://") {
			*images = append(*images, InputImage{URL: strings.TrimSpace(node), kind: imageSourceURL, preview: node})
		}
	case []any:
		for _, item := range node {
			appendInputImageRefs(images, item, allowRaw, mimeHint, filename)
		}
	case map[string]any:
		childMime := mimeHint
		if candidate := stringValue(node["mime_type"]); candidate != "" {
			childMime = candidate
		} else if candidate := stringValue(node["mimeType"]); candidate != "" {
			childMime = candidate
		}
		childName := filename
		if candidate := stringValue(node["filename"]); candidate != "" {
			childName = candidate
		}
		for _, key := range []string{"b64_json", "base64", "data", "image_url", "url", "image"} {
			if child, exists := node[key]; exists {
				appendInputImageRefs(images, child, true, childMime, childName)
			}
		}
	}
}

func inputImageFromString(token scannedString, allowRaw bool, mimeHint, filename string) (InputImage, bool) {
	preview := strings.TrimSpace(token.value)
	lower := strings.ToLower(preview)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if token.truncated {
			return InputImage{}, false
		}
		return InputImage{URL: preview, kind: imageSourceURL, preview: preview}, true
	}
	if strings.HasPrefix(lower, "data:") {
		comma := strings.Index(preview, ",")
		if comma < 0 || !strings.Contains(strings.ToLower(preview[:comma]), ";base64") {
			return InputImage{}, false
		}
		mimeType := strings.TrimPrefix(strings.SplitN(preview[:comma], ";", 2)[0], "data:")
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			mimeType = "image/png"
		}
		return InputImage{
			Filename:  chooseImageFilename(filename, mimeType),
			MIME:      mimeType,
			kind:      imageSourceDataURL,
			rawOffset: token.rawOffset,
			rawLength: token.rawLength,
			preview:   preview,
		}, true
	}
	if allowRaw && token.rawLength > 100 {
		mimeType := strings.TrimSpace(mimeHint)
		if !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
			mimeType = "image/png"
		}
		return InputImage{
			Filename:  chooseImageFilename(filename, mimeType),
			MIME:      mimeType,
			kind:      imageSourceRawBase64,
			rawOffset: token.rawOffset,
			rawLength: token.rawLength,
			preview:   preview,
		}, true
	}
	return InputImage{}, false
}

func chooseImageFilename(filename, mimeType string) string {
	filename = strings.TrimSpace(filename)
	if filename != "" {
		return filename
	}
	switch strings.ToLower(mimeType) {
	case "image/jpeg", "image/jpg":
		return "image.jpg"
	case "image/webp":
		return "image.webp"
	case "image/gif":
		return "image.gif"
	default:
		return "image.png"
	}
}

func dedupeInputImages(images []InputImage, limit int) []InputImage {
	seen := make(map[string]struct{})
	result := make([]InputImage, 0, min(limit, len(images)))
	for _, image := range images {
		marker := image.URL
		if marker == "" {
			marker = fmt.Sprintf("%d:%d:%d", image.kind, image.rawOffset, image.rawLength)
		}
		if marker == "" {
			continue
		}
		if _, exists := seen[marker]; exists {
			continue
		}
		seen[marker] = struct{}{}
		result = append(result, image)
		if len(result) >= limit {
			break
		}
	}
	return result
}
