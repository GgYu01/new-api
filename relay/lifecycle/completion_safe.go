package lifecycle

import (
	"strings"
	"sync"
)

type CompletionSafeMode struct {
	enabled bool
	mu      sync.Mutex
	events  []string
	limit   int
	hasTool bool
}

func NewCompletionSafeMode(enabled bool, limit int) *CompletionSafeMode {
	if limit <= 0 {
		limit = DefaultHoldBufferLimit
	}
	return &CompletionSafeMode{enabled: enabled, limit: limit}
}

func (m *CompletionSafeMode) Enabled() bool {
	return m != nil && m.enabled && !m.hasTool
}

func (m *CompletionSafeMode) Observe(payload string) {
	if m == nil {
		return
	}
	lower := strings.ToLower(payload)
	if strings.Contains(lower, `"type":"response.output_item.added"`) && strings.Contains(lower, `"function"`) {
		m.hasTool = true
		m.Reset()
		return
	}
	if !m.Enabled() {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(strings.Join(m.events, ""))+len(payload) > m.limit {
		m.enabled = false
		m.events = nil
		return
	}
	m.events = append(m.events, payload)
}

func (m *CompletionSafeMode) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.events = nil
	m.mu.Unlock()
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
