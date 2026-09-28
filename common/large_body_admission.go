package common

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/semaphore"
)

const (
	defaultLargeBodyThresholdBytes = int64(8 << 20)
	defaultLargeBodyMaxInFlight    = 16
	defaultLargeBodyWaitTimeout    = 30 * time.Second
)

var ErrLargeBodyAdmissionTimeout = errors.New("large request lane is full")

// LargeBodyAdmissionConfig controls the request-body lane that protects the
// gateway from too many long-lived large uploads. Bodies at or below the
// threshold do not consume a lane slot.
type LargeBodyAdmissionConfig struct {
	ThresholdBytes int64
	MaxInFlight    int
	WaitTimeout    time.Duration
}

// LargeBodyAdmissionStats is a read-only snapshot of the admission lane.
type LargeBodyAdmissionStats struct {
	ThresholdBytes int64  `json:"threshold_bytes"`
	MaxInFlight    int    `json:"max_in_flight"`
	WaitTimeoutMs  int64  `json:"wait_timeout_ms"`
	InFlight       int    `json:"in_flight"`
	PeakInFlight   int    `json:"peak_in_flight"`
	Waiting        int    `json:"waiting"`
	PeakWaiting    int    `json:"peak_waiting"`
	TimeoutTotal   uint64 `json:"timeout_total"`
	CanceledTotal  uint64 `json:"canceled_total"`
}

// LargeBodyAdmission owns a context-aware semaphore for large request bodies.
// A ticket remains held until the associated BodyStorage is closed, so normal
// completion, validation errors, and downstream cancellation share one
// release path.
type LargeBodyAdmission struct {
	thresholdBytes int64
	maxInFlight    int
	waitTimeout    time.Duration
	semaphore      *semaphore.Weighted

	inFlight      atomic.Int64
	peakInFlight  atomic.Int64
	waiting       atomic.Int64
	peakWaiting   atomic.Int64
	timeoutTotal  atomic.Uint64
	canceledTotal atomic.Uint64
}

type largeBodyTicket struct {
	admission *LargeBodyAdmission
	released  atomic.Bool
}

type largeBodyTicketHolder struct {
	mu     sync.Mutex
	ticket *largeBodyTicket
}

type admittedBodyStorage struct {
	BodyStorage
	holder *largeBodyTicketHolder
	once   sync.Once
}

type largeBodyCountingReader struct {
	reader    io.Reader
	ctx       context.Context
	admission *LargeBodyAdmission
	holder    *largeBodyTicketHolder
	read      int64
}

var globalLargeBodyAdmission atomic.Pointer[LargeBodyAdmission]

func init() {
	ConfigureLargeBodyAdmission(LargeBodyAdmissionConfig{})
}

// NewLargeBodyAdmission constructs an independent admission lane. Non-positive
// values use production-safe defaults.
func NewLargeBodyAdmission(config LargeBodyAdmissionConfig) *LargeBodyAdmission {
	if config.ThresholdBytes <= 0 {
		config.ThresholdBytes = defaultLargeBodyThresholdBytes
	}
	if config.MaxInFlight <= 0 {
		config.MaxInFlight = defaultLargeBodyMaxInFlight
	}
	if config.WaitTimeout <= 0 {
		config.WaitTimeout = defaultLargeBodyWaitTimeout
	}
	return &LargeBodyAdmission{
		thresholdBytes: config.ThresholdBytes,
		maxInFlight:    config.MaxInFlight,
		waitTimeout:    config.WaitTimeout,
		semaphore:      semaphore.NewWeighted(int64(config.MaxInFlight)),
	}
}

// ConfigureLargeBodyAdmission replaces the global lane for new requests.
// Requests already holding a ticket continue to release it to their original
// controller.
func ConfigureLargeBodyAdmission(config LargeBodyAdmissionConfig) {
	globalLargeBodyAdmission.Store(NewLargeBodyAdmission(config))
}

// GetLargeBodyAdmissionStats reports the global large-body lane state.
func GetLargeBodyAdmissionStats() LargeBodyAdmissionStats {
	admission := globalLargeBodyAdmission.Load()
	if admission == nil {
		return LargeBodyAdmissionStats{}
	}
	return admission.Stats()
}

// Stats returns a consistent-enough lock-free snapshot for monitoring.
func (a *LargeBodyAdmission) Stats() LargeBodyAdmissionStats {
	if a == nil {
		return LargeBodyAdmissionStats{}
	}
	return LargeBodyAdmissionStats{
		ThresholdBytes: a.thresholdBytes,
		MaxInFlight:    a.maxInFlight,
		WaitTimeoutMs:  a.waitTimeout.Milliseconds(),
		InFlight:       int(a.inFlight.Load()),
		PeakInFlight:   int(a.peakInFlight.Load()),
		Waiting:        int(a.waiting.Load()),
		PeakWaiting:    int(a.peakWaiting.Load()),
		TimeoutTotal:   a.timeoutTotal.Load(),
		CanceledTotal:  a.canceledTotal.Load(),
	}
}

func (a *LargeBodyAdmission) acquire(ctx context.Context) (*largeBodyTicket, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	waiting := a.waiting.Add(1)
	updateAtomicPeak(&a.peakWaiting, waiting)

	acquireContext := ctx
	cancel := func() {}
	if a.waitTimeout > 0 {
		acquireContext, cancel = context.WithTimeout(ctx, a.waitTimeout)
	}
	err := a.semaphore.Acquire(acquireContext, 1)
	cancel()
	a.waiting.Add(-1)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			a.canceledTotal.Add(1)
			return nil, contextErr
		}
		a.timeoutTotal.Add(1)
		return nil, ErrLargeBodyAdmissionTimeout
	}

	inFlight := a.inFlight.Add(1)
	updateAtomicPeak(&a.peakInFlight, inFlight)
	return &largeBodyTicket{admission: a}, nil
}

func (t *largeBodyTicket) release() {
	if t == nil || t.admission == nil || !t.released.CompareAndSwap(false, true) {
		return
	}
	t.admission.inFlight.Add(-1)
	t.admission.semaphore.Release(1)
}

func (h *largeBodyTicketHolder) ensure(ctx context.Context, admission *LargeBodyAdmission) error {
	h.mu.Lock()
	if h.ticket != nil {
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()

	ticket, err := admission.acquire(ctx)
	if err != nil {
		return err
	}
	h.mu.Lock()
	if h.ticket == nil {
		h.ticket = ticket
		h.mu.Unlock()
		return nil
	}
	h.mu.Unlock()
	ticket.release()
	return nil
}

func (h *largeBodyTicketHolder) hasTicket() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ticket != nil
}

func (h *largeBodyTicketHolder) release() {
	h.mu.Lock()
	ticket := h.ticket
	h.ticket = nil
	h.mu.Unlock()
	ticket.release()
}

func (r *largeBodyCountingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	if n <= 0 {
		return n, err
	}
	next := r.read + int64(n)
	if next < r.read {
		return 0, ErrRequestBodyTooLarge
	}
	r.read = next
	if next > r.admission.thresholdBytes {
		if acquireErr := r.holder.ensure(r.ctx, r.admission); acquireErr != nil {
			return 0, acquireErr
		}
	}
	return n, err
}

func (s *admittedBodyStorage) Close() error {
	closeErr := s.BodyStorage.Close()
	s.once.Do(s.holder.release)
	return closeErr
}

// CreateBodyStorageFromReaderWithAdmission creates replayable body storage and
// holds a large-body ticket for the complete storage lifetime. Unknown-length
// and understated bodies are promoted when their actual byte count crosses the
// threshold, so chunked uploads cannot bypass the lane.
func CreateBodyStorageFromReaderWithAdmission(
	ctx context.Context,
	admission *LargeBodyAdmission,
	reader io.Reader,
	contentLength int64,
	maxBytes int64,
) (BodyStorage, error) {
	if admission == nil {
		return CreateBodyStorageFromReader(reader, contentLength, maxBytes)
	}
	if maxBytes < 0 {
		return nil, errors.New("max body size must not be negative")
	}
	if contentLength > maxBytes {
		return nil, ErrRequestBodyTooLarge
	}
	if ctx == nil {
		ctx = context.Background()
	}

	holder := &largeBodyTicketHolder{}
	if contentLength > admission.thresholdBytes {
		if err := holder.ensure(ctx, admission); err != nil {
			return nil, err
		}
	}
	countingReader := &largeBodyCountingReader{
		reader:    reader,
		ctx:       ctx,
		admission: admission,
		holder:    holder,
	}
	storage, err := CreateBodyStorageFromReader(countingReader, contentLength, maxBytes)
	if err != nil {
		holder.release()
		return nil, err
	}
	if storage.Size() <= admission.thresholdBytes {
		holder.release()
		return storage, nil
	}
	if !holder.hasTicket() {
		if err = holder.ensure(ctx, admission); err != nil {
			_ = storage.Close()
			return nil, err
		}
	}
	return &admittedBodyStorage{BodyStorage: storage, holder: holder}, nil
}

func updateAtomicPeak(peak *atomic.Int64, value int64) {
	for {
		current := peak.Load()
		if value <= current || peak.CompareAndSwap(current, value) {
			return
		}
	}
}
