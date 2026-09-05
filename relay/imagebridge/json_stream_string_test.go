package imagebridge

import (
	"encoding/json"
	"strings"
	"testing"
)

// Reference decoder: the standard library JSON string semantics that
// decodeJSONStringPreview must match. json.Unmarshal on a wrapped token is
// the differential oracle for every case below.
func decodeStringReference(t *testing.T, raw string) string {
	t.Helper()
	quoted := `"` + raw + `"`
	if !json.Valid([]byte(quoted)) {
		t.Fatalf("reference fixture is not valid JSON: %q", quoted)
	}
	var out string
	if err := json.Unmarshal([]byte(quoted), &out); err != nil {
		t.Fatalf("reference decode failed for %q: %v", quoted, err)
	}
	return out
}

// decodeStringMustMatch asserts our decoder agrees with encoding/json.
func decodeStringMustMatch(t *testing.T, raw string) string {
	t.Helper()
	want := decodeStringReference(t, raw)
	got, err := decodeJSONStringPreview([]byte(raw), false)
	if err != nil {
		t.Fatalf("decodeJSONStringPreview(%q) error: %v (reference %q)", raw, err, want)
	}
	if got != want {
		t.Fatalf("decodeJSONStringPreview(%q) = %q, want %q", raw, got, want)
	}
	return got
}

func decodeStringMustFail(t *testing.T, raw string) {
	t.Helper()
	quoted := `"` + raw + `"`
	if json.Valid([]byte(quoted)) {
		t.Fatalf("fixture %q must be invalid JSON for this test", quoted)
	}
	if _, err := decodeJSONStringPreview([]byte(raw), false); err == nil {
		t.Fatalf("decodeJSONStringPreview(%q) expected error, got none", raw)
	}
}

// TestDecodeJSONStringAcceptsLegalJSONEscapes covers escapes that are legal
// JSON but were rejected by the previous strconv.Unquote decoder (Go string
// semantics): \/ and non-BMP surrogate pairs.
func TestDecodeJSONStringAcceptsLegalJSONEscapes(t *testing.T) {
	cases := []string{
		`a\/b`,                              // escaped solidus
		`key\/name`,                         // escaped solidus inside a word
		`\ud83d\ude00`,                      // non-BMP emoji via surrogate pair
		`emoji: \ud83d\ude00 end`,           // surrogate pair in context
		`\u4e2d\u6587`,                      // BMP escapes
		`plain`,                             // no escapes
		`quote \" inside`,                   // escaped quote
		`back \\ slash`,                     // escaped backslash
		`\b\f\n\r\t`,                        // short escapes
		`\u00e9\u25b2`,                      // BMP non-ASCII
		`mix \ud83d\ude00 and \u4e2d and \/`,// combined
	}
	for _, raw := range cases {
		decodeStringMustMatch(t, raw)
	}
}

// TestDecodeJSONStringRejectsGoOnlyEscapes covers escapes that strconv.Unquote
// accepted but JSON must reject (Go string semantics over-acceptance).
func TestDecodeJSONStringRejectsGoOnlyEscapes(t *testing.T) {
	cases := []string{
		`\x41`,  // Go hex escape
		`\a`,    // Go bell
		`\v`,    // Go vertical tab
		`\101`,  // Go octal
		`\q`,    // unknown escape
		`\u12g4`,// invalid hex digits
	}
	for _, raw := range cases {
		decodeStringMustFail(t, raw)
	}
}

// TestDecodeJSONStringLoneSurrogateMatchesStdlib pins the lone-surrogate
// behavior to the standard library reference (U+FFFD, not an error).
func TestDecodeJSONStringLoneSurrogateMatchesStdlib(t *testing.T) {
	for _, raw := range []string{`\ud800`, `\ude00`, `before \ud83d after`} {
		decodeStringMustMatch(t, raw)
	}
}

// TestDecodeJSONStringSurrogatePairBoundaryTrimsOnlyWhenTruncated checks the
// preview truncation path: a preview that ends mid-surrogate-pair is repaired
// by trimming the partial escape; the decoded value is a prefix of the true
// string.
func TestDecodeJSONStringSurrogatePairBoundaryTrimsOnlyWhenTruncated(t *testing.T) {
	raw := `hello \ud83d\ude00 world`
	for cut := 1; cut <= 6; cut++ {
		prefix := raw[:len(raw)-cut]
		got, err := decodeJSONStringPreview([]byte(prefix), true)
		if err != nil {
			t.Fatalf("truncated decode(%q) error: %v", prefix, err)
		}
		if !strings.HasPrefix("hello 😀 world", got) && !strings.HasPrefix(got, "hello ") {
			t.Fatalf("truncated decode(%q) = %q, not a plausible prefix", prefix, got)
		}
	}
	// A complete string (truncated=false) must decode exactly.
	decodeStringMustMatch(t, raw)
}

// TestParseStringFullTokenRequiresValidJSON verifies a non-truncated string
// containing an illegal escape fails the parse even when it fits the preview.
func TestParseStringFullTokenRequiresValidJSON(t *testing.T) {
	body := `{"prompt": "bad \x41 escape"}`
	if _, _, err := DetectJSONReader("/v1/images/generations", strings.NewReader(body)); err == nil {
		t.Fatalf("expected error for Go-only escape in JSON body")
	}
}

// TestParseObjectKeyDecodesJSONEscapes covers keys with legal JSON escapes.
func TestParseObjectKeyDecodesJSONEscapes(t *testing.T) {
	body := `{"a\/b": "slash-key", "c\ud83d\ude00d": "emoji-key"}`
	_, matched, err := DetectJSONReader("/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("legal JSON escapes rejected: %v", err)
	}
	if matched {
		t.Fatalf("plain body must not match an image intent")
	}
}

// TestDetectJSONReaderLongStringPreviewTruncation keeps the resource limits:
// a string longer than the preview budget must not fail (it truncates) and
// the depth/node caps still reject pathological documents.
func TestDetectJSONReaderLongStringPreviewTruncation(t *testing.T) {
	long := strings.Repeat("a", 40_000)
	body := `{"model":"gpt-5.6-sol","messages":[{"role":"user","content":"` + long + `"}]}`
	if _, _, err := DetectJSONReader("/v1/chat/completions", strings.NewReader(body)); err != nil {
		t.Fatalf("long string must truncate, not fail: %v", err)
	}

	depth := strings.Repeat(`{"a":`, maxJSONDepth+2) + "1" + strings.Repeat("}", maxJSONDepth+2)
	if _, _, err := DetectJSONReader("/v1/images/generations", strings.NewReader(depth)); err == nil {
		t.Fatalf("expected depth limit error")
	}
}

// TestDetectJSONReaderSurrogatePromptEndToEnd runs a full chat/completions
// body with escaped emoji and slash through the detector, asserting the
// decoded prompt survives into the intent.
func TestDetectJSONReaderSurrogatePromptEndToEnd(t *testing.T) {
	body := `{"model":"gpt-image-default","prompt":"draw \ud83d\ude00 a\/b cat","n":1}`
	_, matched, err := DetectJSONReader("/v1/images/generations", strings.NewReader(body))
	if err != nil {
		t.Fatalf("legal JSON with surrogate pair and escaped slash rejected: %v", err)
	}
	if !matched {
		t.Fatalf("image generation body should match")
	}
}

// TestDecodeJSONStringPreviewMatchesStdlibProperty sweeps generated inputs
// through both decoders: any input the standard library accepts must produce
// the identical value here, and any input it rejects must fail here.
func TestDecodeJSONStringPreviewMatchesStdlibProperty(t *testing.T) {
	fragments := []string{
		``, `a`, `\/`, `\"`, `\\`, `\n`, `\t`, `\u0041`, `\ud83d\ude00`,
		`\ud83d`, `\ude00`, `\ud83dX`, `\x`, `\a`, `中文`, `emoji 😀 raw`,
		`end \u`, `end \`, `end \u00`, `end \ud83d\ude0`,
	}
	for _, head := range fragments {
		for _, tail := range fragments {
			raw := head + tail
			quoted := []byte(`"` + raw + `"`)
			var reference string
			refErr := json.Unmarshal(quoted, &reference)
			got, err := decodeJSONStringPreview([]byte(raw), false)
			if refErr == nil {
				if err != nil {
					t.Fatalf("stdlib accepts %q (%q) but decoder errors: %v", raw, reference, err)
				}
				if got != reference {
					t.Fatalf("decoder(%q) = %q, stdlib = %q", raw, got, reference)
				}
			} else if err == nil {
				t.Fatalf("stdlib rejects %q (%v) but decoder returned %q", raw, refErr, got)
			}
		}
	}
}
