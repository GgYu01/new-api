package imagebridge

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

type replacementSpan struct {
	start       int64
	end         int64
	replacement []byte
}

type offsetReplacementReader struct {
	source      io.Reader
	sourcePos   int64
	spans       []replacementSpan
	currentSpan int
	spanPos     int
}

func newOffsetReplacementReader(source io.Reader, spans []replacementSpan) *offsetReplacementReader {
	// Sort spans by start offset in ascending order
	sort.Slice(spans, func(i, j int) bool {
		return spans[i].start < spans[j].start
	})
	return &offsetReplacementReader{
		source: source,
		spans:  spans,
	}
}

func (r *offsetReplacementReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if r.currentSpan >= len(r.spans) {
			n, err := r.source.Read(p)
			r.sourcePos += int64(n)
			return n, err
		}

		span := r.spans[r.currentSpan]

		// 1. Still before span start: read from source up to span.start
		if r.sourcePos < span.start {
			toRead := int(span.start - r.sourcePos)
			if toRead > len(p) {
				toRead = len(p)
			}
			n, err := r.source.Read(p[:toRead])
			r.sourcePos += int64(n)
			if n > 0 || err != nil {
				return n, err
			}
			continue
		}

		// 2. At span start, emit replacement bytes
		if r.spanPos < len(span.replacement) {
			n := copy(p, span.replacement[r.spanPos:])
			r.spanPos += n
			return n, nil
		}

		// 3. Finished replacement bytes, discard/skip original bytes between start and end
		if r.sourcePos < span.end {
			toDiscard := span.end - r.sourcePos
			if seeker, ok := r.source.(io.Seeker); ok {
				_, err := seeker.Seek(span.end, io.SeekStart)
				if err != nil {
					return 0, err
				}
				r.sourcePos = span.end
			} else {
				discarded, err := io.CopyN(io.Discard, r.source, toDiscard)
				r.sourcePos += discarded
				if err != nil {
					return 0, err
				}
			}
		}

		// Advance to next span
		r.currentSpan++
		r.spanPos = 0
	}
}

// DetectAndRewriteJSONStorage inspects body storage (either memory or disk-backed)
// and rewrites native image tools to private NewAPI planner tools without
// materializing large request bodies into RAM.
func DetectAndRewriteJSONStorage(storage common.BodyStorage, envelope Envelope) (common.BodyStorage, bool, error) {
	if storage == nil {
		return nil, false, nil
	}

	// Quick check: does it contain "tools" and "image"?
	if !storage.IsDisk() {
		raw, err := storage.Bytes()
		if err != nil {
			return storage, false, err
		}
		if !ContainsJSONKeyword(raw, "tools") || !ContainsJSONKeyword(raw, "image") {
			return storage, false, nil
		}
	} else {
		reader, err := storage.NewReader()
		if err != nil {
			return storage, false, err
		}
		hasTools := ContainsJSONKeywordStreaming(reader, "tools")
		_ = reader.Close()
		if !hasTools {
			return storage, false, nil
		}

		reader2, err := storage.NewReader()
		if err != nil {
			return storage, false, err
		}
		hasImage := ContainsJSONKeywordStreaming(reader2, "image")
		_ = reader2.Close()
		if !hasImage {
			return storage, false, nil
		}
	}

	reader, err := storage.NewReader()
	if err != nil {
		return storage, false, err
	}
	defer reader.Close()

	parser := newJSONStreamParser(reader)
	offsets := &RewriterOffsets{}
	parser.offsets = offsets

	_, err = parser.parseDocument()
	if err != nil {
		return storage, false, fmt.Errorf("invalid json in image tool detection: %w", err)
	}

	// If tool_choice is "none", tools rewriting is disabled
	if offsets.ChoiceValue != nil {
		if text := stringValue(offsets.ChoiceValue); text == "none" {
			return storage, false, nil
		}
	}

	var spans []replacementSpan
	deltaSize := int64(0)

	// Inspect tool_choice
	if offsets.ChoiceRange[1] > offsets.ChoiceRange[0] && offsets.ChoiceValue != nil {
		newChoice, changed := rewriteToolChoice(offsets.ChoiceValue)
		if changed {
			newChoiceBytes, err := json.Marshal(newChoice)
			if err != nil {
				return storage, false, err
			}
			oldLen := offsets.ChoiceRange[1] - offsets.ChoiceRange[0]
			spans = append(spans, replacementSpan{
				start:       offsets.ChoiceRange[0],
				end:         offsets.ChoiceRange[1],
				replacement: newChoiceBytes,
			})
			deltaSize += int64(len(newChoiceBytes)) - oldLen
		}
	}

	// Inspect tools: replace or drop image tool element spans without overlapping commas
	if len(offsets.ToolSpans) > 0 {
		hasImageTool := false
		plannerPresent := false
		for _, s := range offsets.ToolSpans {
			if s.IsPlanner {
				plannerPresent = true
			}
			if s.IsImageTool {
				hasImageTool = true
			}
		}

		if hasImageTool {
			type toolAction int
			const (
				actKeep    toolAction = 0
				actReplace toolAction = 1
				actDrop    toolAction = 2
			)

			nTools := len(offsets.ToolSpans)
			actions := make([]toolAction, nTools)
			plannerInserted := plannerPresent

			for i, s := range offsets.ToolSpans {
				if !s.IsImageTool {
					actions[i] = actKeep
					continue
				}
				if !plannerInserted {
					actions[i] = actReplace
					plannerInserted = true
				} else {
					actions[i] = actDrop
				}
			}

			keptCount := 0
			for _, act := range actions {
				if act != actDrop {
					keptCount++
				}
			}

			if keptCount == 0 {
				// All tools dropped: empty the array contents cleanly
				dropStart := offsets.ToolSpans[0].Start
				dropEnd := offsets.ToolSpans[nTools-1].End
				oldLen := dropEnd - dropStart
				spans = append(spans, replacementSpan{
					start:       dropStart,
					end:         dropEnd,
					replacement: nil,
				})
				deltaSize -= oldLen
			} else {
				// 1. Add replacement spans for replaced tools
				for i, act := range actions {
					if act == actReplace {
						s := offsets.ToolSpans[i]
						replacement := plannerToolJSON(envelope)
						oldLen := s.End - s.Start
						spans = append(spans, replacementSpan{
							start:       s.Start,
							end:         s.End,
							replacement: replacement,
						})
						deltaSize += int64(len(replacement)) - oldLen
					}
				}

				// 2. Identify contiguous runs of dropped elements and compute exact non-overlapping drop boundaries
				i := 0
				for i < nTools {
					if actions[i] != actDrop {
						i++
						continue
					}
					runStart := i
					for i < nTools && actions[i] == actDrop {
						i++
					}
					runEnd := i - 1

					var dropStart, dropEnd int64
					if runStart == 0 {
						// Run starts at array beginning: drop elements and the trailing comma before the next element
						dropStart = offsets.ToolSpans[0].Start
						if offsets.ToolSpans[runEnd].SuffixCommaEnd > 0 {
							dropEnd = offsets.ToolSpans[runEnd].SuffixCommaEnd
						} else {
							dropEnd = offsets.ToolSpans[runEnd].End
						}
					} else if runEnd == nTools-1 {
						// Run ends at array end: drop the leading comma before runStart and elements up to end
						if offsets.ToolSpans[runStart].PrefixCommaStart >= 0 {
							dropStart = offsets.ToolSpans[runStart].PrefixCommaStart
						} else {
							dropStart = offsets.ToolSpans[runStart].Start
						}
						dropEnd = offsets.ToolSpans[nTools-1].End
					} else {
						// Run is in middle: keep the comma before runStart, drop from runStart.Start through runEnd.SuffixCommaEnd
						dropStart = offsets.ToolSpans[runStart].Start
						if offsets.ToolSpans[runEnd].SuffixCommaEnd > 0 {
							dropEnd = offsets.ToolSpans[runEnd].SuffixCommaEnd
						} else {
							dropEnd = offsets.ToolSpans[runEnd].End
						}
					}

					oldLen := dropEnd - dropStart
					spans = append(spans, replacementSpan{
						start:       dropStart,
						end:         dropEnd,
						replacement: nil,
					})
					deltaSize -= oldLen
				}
			}
		}
	}

	if len(spans) == 0 {
		return storage, false, nil
	}

	sort.Slice(spans, func(i, j int) bool {
		return spans[i].start < spans[j].start
	})

	var mergedSpans []replacementSpan
	for _, sp := range spans {
		if len(mergedSpans) == 0 {
			mergedSpans = append(mergedSpans, sp)
			continue
		}
		last := &mergedSpans[len(mergedSpans)-1]
		if sp.start <= last.end {
			if len(last.replacement) == 0 && len(sp.replacement) == 0 {
				if sp.end > last.end {
					last.end = sp.end
				}
				continue
			}
		}
		mergedSpans = append(mergedSpans, sp)
	}
	spans = mergedSpans

	deltaSize = int64(0)
	for _, sp := range spans {
		deltaSize += int64(len(sp.replacement)) - (sp.end - sp.start)
	}

	// Rewriting needed: stream through offsetReplacementReader into new storage
	streamReader, err := storage.NewReader()
	if err != nil {
		return storage, false, err
	}
	defer streamReader.Close()

	replacer := newOffsetReplacementReader(streamReader, spans)

	maxMB := constant.MaxRequestBodyMB
	if maxMB <= 0 {
		maxMB = 128
	}
	maxBytes := int64(maxMB) << 20
	newContentLength := storage.Size() + deltaSize
	if newContentLength > maxBytes {
		maxBytes = newContentLength + (1 << 20)
	}

	newStorage, err := common.CreateBodyStorageFromReader(replacer, newContentLength, maxBytes)
	if err != nil {
		return storage, false, fmt.Errorf("failed to create rewritten body storage: %w", err)
	}

	_ = storage.Close()
	return newStorage, true, nil
}

func plannerToolJSON(envelope Envelope) []byte {
	var tool any
	if envelope == EnvelopeResponses {
		tool = map[string]any{
			"type":        "function",
			"name":        PlannerImageToolName,
			"description": "Generate, edit, or create variations of images using AI",
			"parameters":  plannerImageToolParameters(),
		}
	} else {
		tool = map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        PlannerImageToolName,
				"description": "Generate, edit, or create variations of images using AI",
				"parameters":  plannerImageToolParameters(),
			},
		}
	}
	b, _ := json.Marshal(tool)
	return b
}

