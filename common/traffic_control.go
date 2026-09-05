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
	// TrafficControlRevisionOption persists the config revision so stale
	// editors are rejected with 409 even after a process restart. It only
	// changes through the authoritative submit path; loads and periodic syncs
	// never bump it.
	TrafficControlRevisionOption = "TrafficControlRevision"
	// TrafficControlDefaultsMigratedOption records that the one-time switch from
	// the legacy hybrid/120-era defaults to concurrency/240 has been applied.
	// Once present, admin-modified values are never overwritten again.
	TrafficControlDefaultsMigratedOption = "TrafficControlDefaultsMigrated"
)

// TrafficControlDefaultsMigratedValue is the marker persisted after the
// one-time default migration; its absence means the migration has not run.
const TrafficControlDefaultsMigratedValue = "concurrency-240-20260904"

// trafficControlOptionKeys lists every option that participates in the full
// config snapshot so a partial OptionMap can never be published.
var trafficControlOptionKeys = []string{
	TrafficControlEnabledOption, TrafficControlModeOption, TrafficControlGlobalRPMOption,
	TrafficControlBurstOption, TrafficControlMaxActiveOption, TrafficControlWaitingQueueOption,
	TrafficControlWaitingTimeoutMsOption, TrafficControlRevisionOption,
}

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
		Enabled: true, Mode: TrafficControlModeConcurrency, GlobalRPM: 240, Burst: 32,
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

// trafficControlPersistedRevision mirrors the TrafficControlRevision option
// row. It is the authoritative revision reported to clients and used for the
// stale-submit 409 check; it survives process restarts because the option row
// is its source of truth.
var trafficControlPersistedRevision atomic.Uint64

func SetTrafficControlPersistedRevision(revision uint64) {
	trafficControlPersistedRevision.Store(revision)
	if globalTrafficController != nil {
		globalTrafficController.SetRevision(revision)
	}
}

func GetTrafficControlPersistedRevision() uint64 {
	if globalTrafficController != nil {
		return globalTrafficController.Metrics().Revision
	}
	return trafficControlPersistedRevision.Load()
}

// TrafficControlMetrics is a bounded aggregate snapshot; it has no user/key/model labels.
type TrafficControlMetrics struct {
	ActiveCurrent       int64                `json:"active_current"`
	ActivePeak          int64                `json:"active_peak"`
	AdmittedTotal       int64                `json:"admitted_total"`
	RejectedRPMTotal    int64                `json:"rejected_rpm_total"`
	RejectedActiveTotal int64                `json:"rejected_active_total"`
	Revision            uint64               `json:"revision"`
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
	revision    atomic.Uint64
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

func rpmEngaged(mode TrafficControlMode) bool {
	return mode == TrafficControlModeRPM || mode == TrafficControlModeHybrid
}

// UpdateConfig publishes a complete validated configuration. Identical
// configurations are a strict no-op: the token bucket is neither refilled nor
// trimmed and active counters survive. When RPM/Burst change while RPM stays
// engaged, tokens are first refilled at the OLD rate up to now and then clamped
// to the new burst, so frequent saves can never mint extra burst. Entering
// rpm/hybrid from off/concurrency seeds a fresh full bucket.
func (t *TrafficController) UpdateConfig(cfg TrafficControlConfig, now time.Time) error {
	if err := ValidateTrafficControlConfig(cfg); err != nil {
		return err
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cfg == cfg {
		return nil
	}
	old := t.cfg
	if rpmEngaged(cfg.Mode) {
		if rpmEngaged(old.Mode) && old.GlobalRPM > 0 && now.After(t.lastRefill) {
			elapsed := now.Sub(t.lastRefill).Seconds()
			t.tokens += elapsed * float64(old.GlobalRPM) / 60
		}
		t.cfg = cfg
		if rpmEngaged(old.Mode) {
			if t.tokens > float64(cfg.Burst) {
				t.tokens = float64(cfg.Burst)
			}
		} else {
			t.tokens = float64(cfg.Burst)
		}
	} else {
		t.cfg = cfg
	}
	t.lastRefill = now
	t.revision.Add(1)
	return nil
}

func (t *TrafficController) SetRevision(rev uint64) {
	t.revision.Store(rev)
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
// Every admitted request holds an active lease regardless of the configured
// mode, so hot-switching modes and lowering the ceiling always observe the
// true number of in-flight requests.
func (t *TrafficController) Admit(now time.Time) (*TrafficLease, time.Duration, TrafficControlRejectReason) {
	t.mu.Lock()
	cfg := t.cfg
	if cfg.Enabled && (cfg.Mode == TrafficControlModeConcurrency || cfg.Mode == TrafficControlModeHybrid) && cfg.MaxActiveRequests > 0 && t.active.Load() >= cfg.MaxActiveRequests {
		t.mu.Unlock()
		t.rejectedAct.Add(1)
		return nil, time.Second, TrafficControlRejectActive
	}
	if cfg.Enabled && (cfg.Mode == TrafficControlModeRPM || cfg.Mode == TrafficControlModeHybrid) {
		t.refillLocked(now)
		if t.tokens < 1 {
			retry := time.Duration((1 - t.tokens) * 60 / float64(cfg.GlobalRPM) * float64(time.Second))
			if retry < time.Millisecond {
				retry = time.Millisecond
			}
			t.mu.Unlock()
			t.rejectedRPM.Add(1)
			return nil, retry, TrafficControlRejectRPM
		}
		t.tokens--
	}
	current := t.active.Add(1)
	t.mu.Unlock()
	trafficUpdateAtomicPeak(&t.activePeak, current)
	t.admitted.Add(1)
	return &TrafficLease{controller: t}, 0, ""
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
	t.mu.Lock()
	cfg := t.cfg
	rev := t.revision.Load()
	t.mu.Unlock()
	return TrafficControlMetrics{
		ActiveCurrent:       t.active.Load(),
		ActivePeak:          t.activePeak.Load(),
		AdmittedTotal:       t.admitted.Load(),
		RejectedRPMTotal:    t.rejectedRPM.Load(),
		RejectedActiveTotal: t.rejectedAct.Load(),
		Revision:            rev,
		Config:              cfg,
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

// ApplyTrafficControlFromOptions publishes the complete traffic control config
// derived from a full option snapshot. It is the single publish path shared by
// startup, periodic sync, single-option writes and bulk writes; identical
// snapshots are no-ops and never reset the token bucket or active counters.
func ApplyTrafficControlFromOptions(options map[string]string) error {
	cfg, err := TrafficControlConfigFromOptions(options)
	if err != nil {
		return err
	}
	if err := SetTrafficControlConfig(cfg); err != nil {
		return err
	}
	if revStr, ok := options[TrafficControlRevisionOption]; ok {
		if rev, parseErr := strconv.ParseUint(strings.TrimSpace(revStr), 10, 64); parseErr == nil {
			SetTrafficControlPersistedRevision(rev)
		}
	}
	return nil
}

// SnapshotTrafficControlOptions extracts the traffic control option values from
// a live option map. Missing keys fall back to the product defaults so a fresh
// install publishes concurrency/240 instead of failing validation.
func SnapshotTrafficControlOptions(optionMap map[string]string) map[string]string {
	def := DefaultTrafficControlConfig()
	options := map[string]string{
		TrafficControlEnabledOption:          strconv.FormatBool(def.Enabled),
		TrafficControlModeOption:             string(def.Mode),
		TrafficControlGlobalRPMOption:        strconv.FormatInt(def.GlobalRPM, 10),
		TrafficControlBurstOption:            strconv.FormatInt(def.Burst, 10),
		TrafficControlMaxActiveOption:        strconv.FormatInt(def.MaxActiveRequests, 10),
		TrafficControlWaitingQueueOption:     strconv.FormatInt(def.WaitingQueue, 10),
		TrafficControlWaitingTimeoutMsOption: strconv.FormatInt(def.WaitingTimeoutMs, 10),
		TrafficControlRevisionOption:         strconv.FormatUint(GetTrafficControlPersistedRevision(), 10),
	}
	for _, key := range trafficControlOptionKeys {
		if value, ok := optionMap[key]; ok {
			options[key] = value
		}
	}
	return options
}

func AdmitTrafficRequest(now time.Time) (*TrafficLease, time.Duration, TrafficControlRejectReason) {
	return globalTrafficController.Admit(now)
}
