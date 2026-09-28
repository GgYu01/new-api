package imagebridge

import (
	"io"
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

// ContainsJSONKeyword checks whether b contains keyword case-insensitively,
// correctly matching both plain ASCII and RFC 8259 \u00XX hex escapes (e.g. \u0069mage, \u0074ools).
func ContainsJSONKeyword(b []byte, keyword string) bool {
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

// ContainsJSONKeywordStreaming scans a reader in 64KB chunks with an overlap
// window to detect keywords without allocating a contiguous full-file buffer.
func ContainsJSONKeywordStreaming(r io.Reader, keyword string) bool {
	const chunkSize = 64 << 10
	overlap := len(keyword) * 6
	if overlap < 64 {
		overlap = 64
	}
	buf := make([]byte, chunkSize+overlap)
	carry := 0
	for {
		n, err := io.ReadFull(r, buf[carry:])
		readTotal := carry + n
		if readTotal == 0 {
			break
		}
		segment := buf[:readTotal]
		if ContainsJSONKeyword(segment, keyword) {
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

func MayContainImageIntent(raw []byte) bool {
	return ContainsJSONKeyword(raw, "image") ||
		ContainsJSONKeyword(raw, "dall") ||
		ContainsJSONKeyword(raw, "banana")
}

func MayContainImageIntentStreaming(r io.Reader) bool {
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
		if MayContainImageIntent(segment) {
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
