package lifecycle

import (
	"fmt"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	DefaultBodyNoProgressTimeout     = 10 * time.Minute
	DefaultQueueWaitTimeout          = 10 * time.Minute
	DefaultConnectTimeout            = 15 * time.Second
	DefaultTLSHandshakeTimeout       = 30 * time.Second
	DefaultAttemptFirstEventTimeout  = 10 * time.Minute
	DefaultPrecommitRecoveryBudget   = 30 * time.Minute
	DefaultStreamNoProgressTimeout   = 10 * time.Minute
	DefaultWriteNoProgressTimeout    = 10 * time.Minute
	DefaultLogicalRequestMaxLifetime = 60 * time.Minute
	DefaultGracefulDrainTimeout      = 60 * time.Minute
	DefaultSSEHeartbeatInterval      = 15 * time.Second
	DefaultTCPKeepAliveIdle          = 60 * time.Second
	DefaultTCPKeepAliveInterval      = 20 * time.Second
	DefaultTCPKeepAliveCount         = 3
	DefaultMaxUpstreamAttempts       = 8
	DefaultDiagnosticBodyLimit       = 64 << 10
	DefaultHoldBufferLimit           = 1 << 20
)

// Config is the G2/G3 timeout ladder used by the logical-request lifecycle.
// Broad UI/config rollout stays out of this package; values are env-overridable.
type Config struct {
	BodyNoProgressTimeout     time.Duration
	QueueWaitTimeout          time.Duration
	ConnectTimeout            time.Duration
	TLSHandshakeTimeout       time.Duration
	AttemptFirstEventTimeout  time.Duration
	PrecommitRecoveryBudget   time.Duration
	StreamNoProgressTimeout   time.Duration
	WriteNoProgressTimeout    time.Duration
	LogicalRequestMaxLifetime time.Duration
	GracefulDrainTimeout      time.Duration
	SSEHeartbeatInterval      time.Duration
	TCPKeepAliveIdle          time.Duration
	TCPKeepAliveInterval      time.Duration
	TCPKeepAliveCount         int
	MaxUpstreamAttempts       int
	DiagnosticBodyLimit       int
}

func DefaultConfig() Config {
	return Config{
		BodyNoProgressTimeout:     DefaultBodyNoProgressTimeout,
		QueueWaitTimeout:          DefaultQueueWaitTimeout,
		ConnectTimeout:            DefaultConnectTimeout,
		TLSHandshakeTimeout:       DefaultTLSHandshakeTimeout,
		AttemptFirstEventTimeout:  DefaultAttemptFirstEventTimeout,
		PrecommitRecoveryBudget:   DefaultPrecommitRecoveryBudget,
		StreamNoProgressTimeout:   DefaultStreamNoProgressTimeout,
		WriteNoProgressTimeout:    DefaultWriteNoProgressTimeout,
		LogicalRequestMaxLifetime: DefaultLogicalRequestMaxLifetime,
		GracefulDrainTimeout:      DefaultGracefulDrainTimeout,
		SSEHeartbeatInterval:      DefaultSSEHeartbeatInterval,
		TCPKeepAliveIdle:          DefaultTCPKeepAliveIdle,
		TCPKeepAliveInterval:      DefaultTCPKeepAliveInterval,
		TCPKeepAliveCount:         DefaultTCPKeepAliveCount,
		MaxUpstreamAttempts:       DefaultMaxUpstreamAttempts,
		DiagnosticBodyLimit:       DefaultDiagnosticBodyLimit,
	}
}

func LoadConfig() Config {
	ladder := common.LoadTimeoutLadder()
	cfg := Config{
		BodyNoProgressTimeout:     ladder.RequestBodyNoProgressTimeout,
		QueueWaitTimeout:          ladder.NewAPIQueueWaitTimeout,
		ConnectTimeout:            ladder.UpstreamConnectTimeout,
		TLSHandshakeTimeout:       ladder.UpstreamTLSHandshakeTimeout,
		AttemptFirstEventTimeout:  ladder.UpstreamAttemptFirstEventTimeout,
		PrecommitRecoveryBudget:   ladder.PrecommitRecoveryBudget,
		StreamNoProgressTimeout:   ladder.UpstreamStreamNoProgressTimeout,
		WriteNoProgressTimeout:    ladder.DownstreamWriteNoProgressTimeout,
		LogicalRequestMaxLifetime: ladder.LogicalRequestMaxLifetime,
		GracefulDrainTimeout:      ladder.GracefulDrainTimeout,
		SSEHeartbeatInterval:      ladder.SSEHeartbeatInterval,
		TCPKeepAliveIdle:          ladder.TCPKeepaliveIdle,
		TCPKeepAliveInterval:      ladder.TCPKeepaliveInterval,
		TCPKeepAliveCount:         ladder.TCPKeepaliveCount,
		MaxUpstreamAttempts:       ladder.MaxDispatchedUpstreamAttempts,
		DiagnosticBodyLimit:       DefaultDiagnosticBodyLimit,
	}
	if cfg.MaxUpstreamAttempts <= 0 {
		cfg.MaxUpstreamAttempts = DefaultMaxUpstreamAttempts
	}
	if cfg.SSEHeartbeatInterval <= 0 {
		cfg.SSEHeartbeatInterval = DefaultSSEHeartbeatInterval
	}
	if cfg.WriteNoProgressTimeout <= 0 {
		cfg.WriteNoProgressTimeout = DefaultWriteNoProgressTimeout
	}
	return cfg
}

var (
	globalCfg       Config
	globalOnce      sync.Once
	loggedOnce      sync.Once
	testCfgMu       sync.RWMutex
	testCfgOverride *Config
)

func currentConfig() Config {
	testCfgMu.RLock()
	override := testCfgOverride
	testCfgMu.RUnlock()
	if override != nil {
		return *override
	}
	globalOnce.Do(func() {
		globalCfg = LoadConfig()
	})
	return globalCfg
}

// SetTestConfig overrides the process config. Tests must call the returned restore.
func SetTestConfig(cfg Config) func() {
	testCfgMu.Lock()
	prev := testCfgOverride
	cp := cfg
	testCfgOverride = &cp
	testCfgMu.Unlock()
	return func() {
		testCfgMu.Lock()
		testCfgOverride = prev
		testCfgMu.Unlock()
	}
}

func WriteNoProgressTimeout() time.Duration {
	return currentConfig().WriteNoProgressTimeout
}

func logConfigOnce() {
	loggedOnce.Do(func() {
		cfg := currentConfig()
		common.SysLog(fmt.Sprintf(
			"logical request timeouts: body=%s queue=%s connect=%s tls=%s first_event=%s precommit=%s stream_idle=%s write=%s lifetime=%s drain=%s sse_hb=%s attempts=%d",
			cfg.BodyNoProgressTimeout,
			cfg.QueueWaitTimeout,
			cfg.ConnectTimeout,
			cfg.TLSHandshakeTimeout,
			cfg.AttemptFirstEventTimeout,
			cfg.PrecommitRecoveryBudget,
			cfg.StreamNoProgressTimeout,
			cfg.WriteNoProgressTimeout,
			cfg.LogicalRequestMaxLifetime,
			cfg.GracefulDrainTimeout,
			cfg.SSEHeartbeatInterval,
			cfg.MaxUpstreamAttempts,
		))
	})
}
