package lifecycle

import (
	"os"
	"strings"
	"sync"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
)

const (
	FallbackNone     = ""
	FallbackRealtime = "realtime"

	ReasonDisabled          = "disabled"
	ReasonSideEffectTools   = "side_effect_tools"
	ReasonAudioIntent       = "audio_intent"
	ReasonVideoIntent       = "video_intent"
	ReasonLargeImageIntent  = "large_image_intent"
	ReasonSpoolOverflow     = "spool_size_overflow"
	ReasonDiskFull          = "disk_full"
	ReasonNotResponses      = "not_responses"
	ReasonCompleted         = "completed"
	ReasonAttemptIncomplete = "attempt_incomplete"

	// LargeImageIntentBytes is the eligibility cutoff for "large-image" intent.
	// Decided before any semantic commit; oversized image bodies never enter
	// completion-safe spool.
	LargeImageIntentBytes = int64(1 << 20)
)

// CompletionSafeEnabled reports the process default. Completion-safe is off
// unless COMPLETION_SAFE or COMPLETION_SAFE_ENABLED is an explicit true value.
func CompletionSafeEnabled() bool {
	if v, ok := os.LookupEnv("COMPLETION_SAFE"); ok {
		return parseBoolEnv(v)
	}
	if v, ok := os.LookupEnv("COMPLETION_SAFE_ENABLED"); ok {
		return parseBoolEnv(v)
	}
	return false
}

func CompletionSafeLimit() int {
	n := common.GetEnvOrDefault("COMPLETION_SAFE_LIMIT_BYTES", DefaultHoldBufferLimit)
	if n <= 0 {
		return DefaultHoldBufferLimit
	}
	return n
}

func parseBoolEnv(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// EligibilityInput is the pre-commit snapshot used to accept or refuse
// completion-safe spooling. The decision is made once, before any semantic
// bytes are exposed, and never flipped mid-commit.
type EligibilityInput struct {
	Enabled            bool
	Format             types.RelayFormat
	Path               string
	HasSideEffectTools bool
	HasAudio           bool
	HasVideo           bool
	HasLargeImage      bool
	ImageBytes         int64
	SpoolBytes         int
	SpoolLimit         int
	DiskFull           bool
}

// EligibilityDecision is the frozen spool/realtime choice.
type EligibilityDecision struct {
	Eligible bool   `json:"eligible"`
	Fallback string `json:"fallback,omitempty"`
	Reason   string `json:"reason"`
}

func EvaluateEligibility(in EligibilityInput) EligibilityDecision {
	if !in.Enabled {
		return EligibilityDecision{Eligible: false, Fallback: FallbackRealtime, Reason: ReasonDisabled}
	}
	if in.Format != "" && in.Format != types.RelayFormatOpenAIResponses {
		return EligibilityDecision{Eligible: false, Fallback: FallbackRealtime, Reason: ReasonNotResponses}
	}
	if in.Path != "" && in.Path != "/v1/responses" && !strings.HasPrefix(in.Path, "/v1/responses") {
		return EligibilityDecision{Eligible: false, Fallback: FallbackRealtime, Reason: ReasonNotResponses}
	}
	if in.HasSideEffectTools {
		return EligibilityDecision{Eligible: false, Fallback: FallbackRealtime, Reason: ReasonSideEffectTools}
	}
	if in.HasAudio {
		return EligibilityDecision{Eligible: false, Fallback: FallbackRealtime, Reason: ReasonAudioIntent}
	}
	if in.HasVideo {
		return EligibilityDecision{Eligible: false, Fallback: FallbackRealtime, Reason: ReasonVideoIntent}
	}
	if in.HasLargeImage || in.ImageBytes >= LargeImageIntentBytes {
		return EligibilityDecision{Eligible: false, Fallback: FallbackRealtime, Reason: ReasonLargeImageIntent}
	}
	if in.DiskFull {
		return EligibilityDecision{Eligible: false, Fallback: FallbackRealtime, Reason: ReasonDiskFull}
	}
	limit := in.SpoolLimit
	if limit <= 0 {
		limit = DefaultHoldBufferLimit
	}
	if in.SpoolBytes > 0 && in.SpoolBytes > limit {
		return EligibilityDecision{Eligible: false, Fallback: FallbackRealtime, Reason: ReasonSpoolOverflow}
	}
	return EligibilityDecision{Eligible: true, Reason: "eligible"}
}

type CompletionSafeMode struct {
	enabled  bool
	mu       sync.Mutex
	events   []string
	limit    int
	size     int
	hasTool  bool
	flushed  bool
	closed   bool
	fallback string
	reason   string
}

func NewCompletionSafeMode(enabled bool, limit int) *CompletionSafeMode {
	if limit <= 0 {
		limit = DefaultHoldBufferLimit
	}
	reason := ReasonDisabled
	if enabled {
		reason = "eligible"
	}
	return &CompletionSafeMode{enabled: enabled, limit: limit, reason: reason}
}

func (m *CompletionSafeMode) Enabled() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled && !m.hasTool && !m.closed && !m.flushed
}

func (m *CompletionSafeMode) Fallback() string {
	if m == nil {
		return FallbackNone
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.fallback
}

func (m *CompletionSafeMode) Reason() string {
	if m == nil {
		return ""
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reason
}

func (m *CompletionSafeMode) ApplyDecision(d EligibilityDecision) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reason = d.Reason
	if !d.Eligible {
		m.enabled = false
		m.fallback = d.Fallback
		if m.fallback == "" {
			m.fallback = FallbackRealtime
		}
		m.events = nil
		m.size = 0
		return
	}
	m.enabled = true
	m.fallback = FallbackNone
}

func (m *CompletionSafeMode) ShouldHold(payload string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled || m.hasTool || m.closed || m.flushed {
		return false
	}
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return false
	}
	if isKeepaliveSSE([]byte(trimmed)) || isSSEComment([]byte(trimmed)) {
		return false
	}
	return true
}

func (m *CompletionSafeMode) Observe(payload string) {
	_ = m.ObserveFrame(payload)
}

// ObserveFrame records one downstream frame. It returns false when the frame
// must pass through live (disabled, overflow fallback, or tool fallback).
func (m *CompletionSafeMode) ObserveFrame(payload string) (held bool) {
	if m == nil {
		return false
	}
	lower := strings.ToLower(payload)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.flushed {
		return false
	}
	if strings.Contains(lower, `"type":"response.output_item.added"`) && strings.Contains(lower, `"function"`) {
		m.hasTool = true
		m.enabled = false
		m.fallback = FallbackRealtime
		m.reason = ReasonSideEffectTools
		m.events = nil
		m.size = 0
		return false
	}
	if !m.enabled {
		return false
	}
	if m.size+len(payload) > m.limit {
		// Overflow is decided before semantic commit: drop the spool and fall
		// back to realtime without exposing any held bytes.
		m.enabled = false
		m.fallback = FallbackRealtime
		m.reason = ReasonSpoolOverflow
		m.events = nil
		m.size = 0
		return false
	}
	m.events = append(m.events, payload)
	m.size += len(payload)
	return true
}

func (m *CompletionSafeMode) HasCompleted() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	joined := strings.Join(m.events, "")
	return strings.Contains(joined, `"type":"response.completed"`) ||
		strings.Contains(joined, "event: response.completed")
}

func (m *CompletionSafeMode) MarkFlushed() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.flushed = true
	m.reason = ReasonCompleted
	m.mu.Unlock()
}

func (m *CompletionSafeMode) Flushed() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.flushed
}

func (m *CompletionSafeMode) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.events = nil
	m.size = 0
	m.flushed = false
	if m.reason == ReasonCompleted || m.reason == ReasonAttemptIncomplete {
		if m.enabled {
			m.reason = "eligible"
		}
	}
	m.mu.Unlock()
}

func (m *CompletionSafeMode) Close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.closed = true
	m.enabled = false
	m.events = nil
	m.size = 0
	m.mu.Unlock()
}

func (m *CompletionSafeMode) Closed() bool {
	return m != nil && func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.closed
	}()
}

func (m *CompletionSafeMode) Replay() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.events))
	copy(out, m.events)
	return out
}

func (lr *LogicalRequest) SetCompletionSafe(m *CompletionSafeMode) {
	if lr == nil {
		return
	}
	lr.mu.Lock()
	lr.completionSafe = m
	lr.mu.Unlock()
}

func (lr *LogicalRequest) CompletionSafe() *CompletionSafeMode {
	if lr == nil {
		return nil
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	return lr.completionSafe
}

func (lr *LogicalRequest) ApplyCompletionSafeEligibility(in EligibilityInput) EligibilityDecision {
	d := EvaluateEligibility(in)
	if lr == nil {
		return d
	}
	lr.mu.Lock()
	cs := lr.completionSafe
	lr.mu.Unlock()
	if cs != nil {
		cs.ApplyDecision(d)
	}
	return d
}

func (w *Writer) emitLocked(data []byte) (int, error) {
	if !w.headerSent {
		w.writeHeaderLocked(w.status)
	}
	n, err := w.ResponseWriter.Write(data)
	if w.size < 0 {
		w.size = 0
	}
	w.size += n
	if w.lr != nil && n > 0 {
		classifyWrite(w.lr, data[:n])
	}
	return n, err
}

func (w *Writer) emitHeldLocked(cs *CompletionSafeMode) error {
	if cs == nil {
		return nil
	}
	for _, frame := range cs.Replay() {
		if _, err := w.emitLocked([]byte(frame)); err != nil {
			return err
		}
	}
	return nil
}

func (lr *LogicalRequest) interceptCompletionSafe(w *Writer, data []byte) (handled bool, n int, err error) {
	if lr == nil || w == nil {
		return false, 0, nil
	}
	cs := lr.CompletionSafe()
	if cs == nil || !cs.ShouldHold(string(data)) {
		return false, 0, nil
	}
	if !cs.ObserveFrame(string(data)) {
		if flushErr := w.emitHeldLocked(cs); flushErr != nil {
			return true, 0, flushErr
		}
		cs.Reset()
		return false, 0, nil
	}
	if !w.headerSent {
		w.writeHeaderLocked(w.status)
	}
	lr.MarkKeepaliveOnly()
	if cs.HasCompleted() {
		if flushErr := w.emitHeldLocked(cs); flushErr != nil {
			return true, 0, flushErr
		}
		cs.MarkFlushed()
		lr.MarkSemanticCommitted()
	}
	return true, len(data), nil
}

func initCompletionSafe(lr *LogicalRequest, format types.RelayFormat) {
	if lr == nil || !CompletionSafeEnabled() {
		return
	}
	if format != types.RelayFormatOpenAIResponses {
		return
	}
	lr.completionSafe = NewCompletionSafeMode(true, CompletionSafeLimit())
}
