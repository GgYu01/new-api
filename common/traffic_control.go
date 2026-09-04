package common

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// TrafficControlMode selects which global admission controls are active.
type TrafficControlMode string

const (
	TrafficControlModeOff         TrafficControlMode = "off"
	TrafficControlModeRPM         TrafficControlMode = "rpm"
	TrafficControlModeConcurrency TrafficControlMode = "concurrency"
	TrafficControlModeHybrid      TrafficControlMode = "hybrid"
)

const (
	TrafficControlEnabledOption          = "TrafficControlEnabled"
	TrafficControlModeOption             = "TrafficControlMode"
	TrafficControlGlobalRPMOption        = "TrafficControlGlobalRPM"
	TrafficControlBurstOption            = "TrafficControlBurst"
	TrafficControlMaxActiveOption        = "TrafficControlMaxActiveRequests"
	TrafficControlWaitingQueueOption     = "TrafficControlWaitingQueue"
	TrafficControlWaitingTimeoutMsOption = "TrafficControlWaitingTimeoutMs"
)

// TrafficControlConfig is intentionally small and immutable after publication.
// Waiting queues are reserved for a later phase and must remain zero.
type TrafficControlConfig struct {
	Enabled           bool               `json:"enabled"`
	Mode              TrafficControlMode `json:"mode"`
	GlobalRPM         int64              `json:"global_rpm"`
	Burst             int64              `json:"burst"`
	MaxActiveRequests int64              `json:"max_active_requests"`
	WaitingQueue      int64              `json:"waiting_queue"`
	WaitingTimeoutMs  int64              `json:"waiting_timeout_ms"`
}

func DefaultTrafficControlConfig() TrafficControlConfig {
	return TrafficControlConfig{
		Enabled: true, Mode: TrafficControlModeHybrid, GlobalRPM: 240, Burst: 32,
		MaxActiveRequests: 240, WaitingQueue: 0, WaitingTimeoutMs: 0,
	}
}

func ValidateTrafficControlConfig(cfg TrafficControlConfig) error {
	switch cfg.Mode {
	case TrafficControlModeOff, TrafficControlModeRPM, TrafficControlModeConcurrency, TrafficControlModeHybrid:
	default:
		return fmt.Errorf("invalid traffic control mode %q", cfg.Mode)
	}
	if cfg.GlobalRPM < 0 || cfg.Burst < 0 || cfg.MaxActiveRequests < 0 || cfg.WaitingQueue < 0 || cfg.WaitingTimeoutMs < 0 {
		return fmt.Errorf("traffic control values must be non-negative")
	}
	if cfg.WaitingQueue != 0 || cfg.WaitingTimeoutMs != 0 {
		return fmt.Errorf("waiting queue is disabled in this phase")
	}
	if (cfg.Mode == TrafficControlModeRPM || cfg.Mode == TrafficControlModeHybrid) && (cfg.GlobalRPM <= 0 || cfg.Burst <= 0) {
		return fmt.Errorf("rpm and burst must be positive in %s mode", cfg.Mode)
	}
	if (cfg.Mode == TrafficControlModeConcurrency || cfg.Mode == TrafficControlModeHybrid) && cfg.MaxActiveRequests <= 0 {
		return fmt.Errorf("max active requests must be positive in %s mode", cfg.Mode)
	}
	return nil
}

// TrafficControlMetrics is a bounded aggregate snapshot; it has no user/key/model labels.
type TrafficControlMetrics struct {
	ActiveCurrent       int64                `json:"active_current"`
	ActivePeak          int64                `json:"active_peak"`
	AdmittedTotal       int64                `json:"admitted_total"`
	RejectedRPMTotal    int64                `json:"rejected_rpm_total"`
	RejectedActiveTotal int64                `json:"rejected_active_total"`
	Config              TrafficControlConfig `json:"config"`
}

type TrafficControlRejectReason string

const (
	TrafficControlRejectRPM    TrafficControlRejectReason = "rpm"
	TrafficControlRejectActive TrafficControlRejectReason = "active"
)

// TrafficController owns one global token bucket and active counter. The clock is
// supplied to Admit so deterministic tests can exercise refill boundaries.
type TrafficController struct {
	mu          sync.Mutex
	cfg         TrafficControlConfig
	tokens      float64
	lastRefill  time.Time
	active      atomic.Int64
	activePeak  atomic.Int64
	admitted    atomic.Int64
	rejectedRPM atomic.Int64
	rejectedAct atomic.Int64
}

func NewTrafficController(cfg TrafficControlConfig, now time.Time) (*TrafficController, error) {
	if err := ValidateTrafficControlConfig(cfg); err != nil {
		return nil, err
	}
	return &TrafficController{cfg: cfg, tokens: float64(cfg.Burst), lastRefill: now}, nil
}

func (t *TrafficController) Config() TrafficControlConfig {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cfg
}

func (t *TrafficController) UpdateConfig(cfg TrafficControlConfig, now time.Time) error {
	if err := ValidateTrafficControlConfig(cfg); err != nil {
		return err
	}
	t.mu.Lock()
	t.cfg, t.tokens, t.lastRefill = cfg, float64(cfg.Burst), now
	t.mu.Unlock()
	return nil
}

func (t *TrafficController) refillLocked(now time.Time) {
	if now.Before(t.lastRefill) {
		t.lastRefill = now
		return
	}
	if t.cfg.GlobalRPM <= 0 {
		return
	}
	elapsed := now.Sub(t.lastRefill).Seconds()
	t.tokens += elapsed * float64(t.cfg.GlobalRPM) / 60
	if t.tokens > float64(t.cfg.Burst) {
		t.tokens = float64(t.cfg.Burst)
	}
	t.lastRefill = now
}

// Admit performs O(1) admission. A nil lease means the caller must reject.
func (t *TrafficController) Admit(now time.Time) (*TrafficLease, time.Duration, TrafficControlRejectReason) {
	t.mu.Lock()
	defer t.mu.Unlock()
	cfg := t.cfg
	if !cfg.Enabled || cfg.Mode == TrafficControlModeOff {
		t.admitted.Add(1)
		return &TrafficLease{}, 0, ""
	}
	if (cfg.Mode == TrafficControlModeConcurrency || cfg.Mode == TrafficControlModeHybrid) && cfg.MaxActiveRequests > 0 && t.active.Load() >= cfg.MaxActiveRequests {
		t.rejectedAct.Add(1)
		return nil, time.Second, TrafficControlRejectActive
	}
	if cfg.Mode == TrafficControlModeRPM || cfg.Mode == TrafficControlModeHybrid {
		t.refillLocked(now)
		if t.tokens < 1 {
			retry := time.Duration((1 - t.tokens) * 60 / float64(cfg.GlobalRPM) * float64(time.Second))
			if retry < time.Millisecond {
				retry = time.Millisecond
			}
			t.rejectedRPM.Add(1)
			return nil, retry, TrafficControlRejectRPM
		}
		t.tokens--
	}
	if cfg.Mode == TrafficControlModeConcurrency || cfg.Mode == TrafficControlModeHybrid {
		current := t.active.Add(1)
		trafficUpdateAtomicPeak(&t.activePeak, current)
		t.admitted.Add(1)
		return &TrafficLease{controller: t}, 0, ""
	}
	t.admitted.Add(1)
	return &TrafficLease{}, 0, ""
}

func trafficUpdateAtomicPeak(peak *atomic.Int64, value int64) {
	for {
		old := peak.Load()
		if value <= old || peak.CompareAndSwap(old, value) {
			return
		}
	}
}

func (t *TrafficController) release() { t.active.Add(-1) }

type TrafficLease struct {
	controller *TrafficController
	once       sync.Once
}

func (l *TrafficLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.controller != nil {
			l.controller.release()
		}
	})
}

func (t *TrafficController) Metrics() TrafficControlMetrics {
	return TrafficControlMetrics{
		ActiveCurrent: t.active.Load(), ActivePeak: t.activePeak.Load(), AdmittedTotal: t.admitted.Load(),
		RejectedRPMTotal: t.rejectedRPM.Load(), RejectedActiveTotal: t.rejectedAct.Load(), Config: t.Config(),
	}
}

var globalTrafficController, _ = NewTrafficController(DefaultTrafficControlConfig(), time.Now())

func TrafficControlConfigFromOptions(options map[string]string) (TrafficControlConfig, error) {
	cfg := DefaultTrafficControlConfig()
	parseBool := func(key string, dst *bool) error {
		if value, ok := options[key]; ok {
			parsed, err := strconv.ParseBool(strings.TrimSpace(value))
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			*dst = parsed
		}
		return nil
	}
	parseInt := func(key string, dst *int64) error {
		if value, ok := options[key]; ok {
			parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
			*dst = parsed
		}
		return nil
	}
	if err := parseBool(TrafficControlEnabledOption, &cfg.Enabled); err != nil {
		return cfg, err
	}
	if value, ok := options[TrafficControlModeOption]; ok {
		cfg.Mode = TrafficControlMode(strings.ToLower(strings.TrimSpace(value)))
	}
	for key, dst := range map[string]*int64{TrafficControlGlobalRPMOption: &cfg.GlobalRPM, TrafficControlBurstOption: &cfg.Burst, TrafficControlMaxActiveOption: &cfg.MaxActiveRequests, TrafficControlWaitingQueueOption: &cfg.WaitingQueue, TrafficControlWaitingTimeoutMsOption: &cfg.WaitingTimeoutMs} {
		if err := parseInt(key, dst); err != nil {
			return cfg, err
		}
	}
	return cfg, ValidateTrafficControlConfig(cfg)
}

func GetTrafficControlMetrics() TrafficControlMetrics { return globalTrafficController.Metrics() }
func GetTrafficControlConfig() TrafficControlConfig   { return globalTrafficController.Config() }
func SetTrafficControlConfig(cfg TrafficControlConfig) error {
	return globalTrafficController.UpdateConfig(cfg, time.Now())
}

// ApplyTrafficControlOption updates only the changed option and publishes a new
// immutable snapshot. It is called by the existing option persistence path.
func ApplyTrafficControlOption(key, value string) error {
	if key != TrafficControlEnabledOption && key != TrafficControlModeOption && key != TrafficControlGlobalRPMOption && key != TrafficControlBurstOption && key != TrafficControlMaxActiveOption && key != TrafficControlWaitingQueueOption && key != TrafficControlWaitingTimeoutMsOption {
		return nil
	}
	current := GetTrafficControlConfig()
	options := map[string]string{
		TrafficControlEnabledOption: strconv.FormatBool(current.Enabled), TrafficControlModeOption: string(current.Mode),
		TrafficControlGlobalRPMOption: strconv.FormatInt(current.GlobalRPM, 10), TrafficControlBurstOption: strconv.FormatInt(current.Burst, 10),
		TrafficControlMaxActiveOption: strconv.FormatInt(current.MaxActiveRequests, 10), TrafficControlWaitingQueueOption: strconv.FormatInt(current.WaitingQueue, 10),
		TrafficControlWaitingTimeoutMsOption: strconv.FormatInt(current.WaitingTimeoutMs, 10),
	}
	options[key] = value
	cfg, err := TrafficControlConfigFromOptions(options)
	if err != nil {
		return err
	}
	return SetTrafficControlConfig(cfg)
}

func AdmitTrafficRequest(now time.Time) (*TrafficLease, time.Duration, TrafficControlRejectReason) {
	return globalTrafficController.Admit(now)
}
