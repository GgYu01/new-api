package lifecycle

import "sync"

// HoldBuffer stores failed-attempt stream events (response.created / id / usage)
// until a real semantic commit. Bytes are discarded on attempt failure.
type HoldBuffer struct {
	mu      sync.Mutex
	chunks  []string
	size    int
	limit   int
	dropped bool
}

func NewHoldBuffer(limit int) *HoldBuffer {
	if limit <= 0 {
		limit = DefaultHoldBufferLimit
	}
	return &HoldBuffer{limit: limit}
}

func (h *HoldBuffer) Push(data string) bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	n := len(data)
	if h.size+n > h.limit {
		h.dropped = true
		return false
	}
	h.chunks = append(h.chunks, data)
	h.size += n
	return true
}

func (h *HoldBuffer) Reset() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.chunks = nil
	h.size = 0
	h.dropped = false
}

func (h *HoldBuffer) Drain() []string {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	out := h.chunks
	h.chunks = nil
	h.size = 0
	return out
}

func (lr *LogicalRequest) Hold() *HoldBuffer {
	if lr == nil {
		return nil
	}
	lr.mu.Lock()
	defer lr.mu.Unlock()
	if lr.hold == nil {
		lr.hold = NewHoldBuffer(DefaultHoldBufferLimit)
	}
	return lr.hold
}

func (lr *LogicalRequest) ResetHold() {
	if lr == nil {
		return
	}
	if h := lr.Hold(); h != nil {
		h.Reset()
	}
}
