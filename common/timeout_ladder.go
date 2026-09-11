package common

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Timeout ladder defaults from the R13 Long-Haul contract. These are
// process-wide stage budgets, not a single http.Client.Timeout.
const (
	DefaultRequestBodyNoProgressTimeout     = 10 * time.Minute
	DefaultNewAPIQueueWaitTimeout           = 10 * time.Minute
	DefaultUpstreamConnectTimeout           = 15 * time.Second
	DefaultUpstreamTLSHandshakeTimeout      = 30 * time.Second
	DefaultUpstreamAttemptFirstEventTimeout = 10 * time.Minute
	DefaultPrecommitRecoveryBudget          = 30 * time.Minute
	DefaultUpstreamStreamNoProgressTimeout  = 10 * time.Minute
	DefaultDownstreamWriteNoProgressTimeout = 10 * time.Minute
	DefaultLogicalRequestMaxLifetime        = 60 * time.Minute
	DefaultGracefulDrainTimeout             = 60 * time.Minute
	DefaultSSEHeartbeatInterval             = 15 * time.Second
	DefaultTCPKeepaliveIdle                 = 60 * time.Second
	DefaultTCPKeepaliveInterval             = 20 * time.Second
	DefaultTCPKeepaliveCount                = 3
	DefaultMaxDispatchedUpstreamAttempts    = 8
)

type TimeoutLadder struct {
	RequestBodyNoProgressTimeout     time.Duration
	NewAPIQueueWaitTimeout           time.Duration
	UpstreamConnectTimeout           time.Duration
	UpstreamTLSHandshakeTimeout      time.Duration
	UpstreamAttemptFirstEventTimeout time.Duration
	PrecommitRecoveryBudget          time.Duration
	UpstreamStreamNoProgressTimeout  time.Duration
	DownstreamWriteNoProgressTimeout time.Duration
	LogicalRequestMaxLifetime        time.Duration
	GracefulDrainTimeout             time.Duration
	SSEHeartbeatInterval             time.Duration
	TCPKeepaliveIdle                 time.Duration
	TCPKeepaliveInterval             time.Duration
	TCPKeepaliveCount                int
	MaxDispatchedUpstreamAttempts    int
}

func DefaultTimeoutLadder() TimeoutLadder {
	return TimeoutLadder{
		RequestBodyNoProgressTimeout:     DefaultRequestBodyNoProgressTimeout,
		NewAPIQueueWaitTimeout:           DefaultNewAPIQueueWaitTimeout,
		UpstreamConnectTimeout:           DefaultUpstreamConnectTimeout,
		UpstreamTLSHandshakeTimeout:      DefaultUpstreamTLSHandshakeTimeout,
		UpstreamAttemptFirstEventTimeout: DefaultUpstreamAttemptFirstEventTimeout,
		PrecommitRecoveryBudget:          DefaultPrecommitRecoveryBudget,
		UpstreamStreamNoProgressTimeout:  DefaultUpstreamStreamNoProgressTimeout,
		DownstreamWriteNoProgressTimeout: DefaultDownstreamWriteNoProgressTimeout,
		LogicalRequestMaxLifetime:        DefaultLogicalRequestMaxLifetime,
		GracefulDrainTimeout:             DefaultGracefulDrainTimeout,
		SSEHeartbeatInterval:             DefaultSSEHeartbeatInterval,
		TCPKeepaliveIdle:                 DefaultTCPKeepaliveIdle,
		TCPKeepaliveInterval:             DefaultTCPKeepaliveInterval,
		TCPKeepaliveCount:                DefaultTCPKeepaliveCount,
		MaxDispatchedUpstreamAttempts:    DefaultMaxDispatchedUpstreamAttempts,
	}
}

func durationEnv(name string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func positiveIntEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func LoadTimeoutLadder() TimeoutLadder {
	ladder := DefaultTimeoutLadder()
	ladder.RequestBodyNoProgressTimeout = durationEnv("REQUEST_BODY_NO_PROGRESS_TIMEOUT", ladder.RequestBodyNoProgressTimeout)
	ladder.NewAPIQueueWaitTimeout = durationEnv("NEWAPI_QUEUE_WAIT_TIMEOUT", ladder.NewAPIQueueWaitTimeout)
	ladder.UpstreamConnectTimeout = durationEnv("UPSTREAM_CONNECT_TIMEOUT", ladder.UpstreamConnectTimeout)
	ladder.UpstreamTLSHandshakeTimeout = durationEnv("UPSTREAM_TLS_HANDSHAKE_TIMEOUT", ladder.UpstreamTLSHandshakeTimeout)
	ladder.UpstreamAttemptFirstEventTimeout = durationEnv("UPSTREAM_ATTEMPT_FIRST_EVENT_TIMEOUT", ladder.UpstreamAttemptFirstEventTimeout)
	ladder.PrecommitRecoveryBudget = durationEnv("PRECOMMIT_RECOVERY_BUDGET", ladder.PrecommitRecoveryBudget)
	ladder.UpstreamStreamNoProgressTimeout = durationEnv("UPSTREAM_STREAM_NO_PROGRESS_TIMEOUT", ladder.UpstreamStreamNoProgressTimeout)
	ladder.DownstreamWriteNoProgressTimeout = durationEnv("DOWNSTREAM_WRITE_NO_PROGRESS_TIMEOUT", ladder.DownstreamWriteNoProgressTimeout)
	ladder.LogicalRequestMaxLifetime = durationEnv("LOGICAL_REQUEST_MAX_LIFETIME", ladder.LogicalRequestMaxLifetime)
	ladder.GracefulDrainTimeout = durationEnv("GRACEFUL_DRAIN_TIMEOUT", ladder.GracefulDrainTimeout)
	ladder.SSEHeartbeatInterval = durationEnv("SSE_HEARTBEAT_INTERVAL", ladder.SSEHeartbeatInterval)
	ladder.TCPKeepaliveIdle = durationEnv("TCP_KEEPALIVE_IDLE", ladder.TCPKeepaliveIdle)
	ladder.TCPKeepaliveInterval = durationEnv("TCP_KEEPALIVE_INTERVAL", ladder.TCPKeepaliveInterval)
	ladder.TCPKeepaliveCount = positiveIntEnv("TCP_KEEPALIVE_COUNT", ladder.TCPKeepaliveCount)
	ladder.MaxDispatchedUpstreamAttempts = positiveIntEnv("MAX_DISPATCHED_UPSTREAM_ATTEMPTS", ladder.MaxDispatchedUpstreamAttempts)
	return ladder
}

func (l TimeoutLadder) AttemptFirstEventDeadline(rootRemaining time.Duration) time.Duration {
	d := l.UpstreamAttemptFirstEventTimeout
	if rootRemaining > 0 && rootRemaining < d {
		return rootRemaining
	}
	return d
}

func (l TimeoutLadder) View() map[string]any {
	return map[string]any{
		"REQUEST_BODY_NO_PROGRESS_TIMEOUT":     l.RequestBodyNoProgressTimeout.String(),
		"NEWAPI_QUEUE_WAIT_TIMEOUT":            l.NewAPIQueueWaitTimeout.String(),
		"UPSTREAM_CONNECT_TIMEOUT":             l.UpstreamConnectTimeout.String(),
		"UPSTREAM_TLS_HANDSHAKE_TIMEOUT":       l.UpstreamTLSHandshakeTimeout.String(),
		"UPSTREAM_ATTEMPT_FIRST_EVENT_TIMEOUT": l.UpstreamAttemptFirstEventTimeout.String(),
		"PRECOMMIT_RECOVERY_BUDGET":            l.PrecommitRecoveryBudget.String(),
		"UPSTREAM_STREAM_NO_PROGRESS_TIMEOUT":  l.UpstreamStreamNoProgressTimeout.String(),
		"DOWNSTREAM_WRITE_NO_PROGRESS_TIMEOUT": l.DownstreamWriteNoProgressTimeout.String(),
		"LOGICAL_REQUEST_MAX_LIFETIME":         l.LogicalRequestMaxLifetime.String(),
		"GRACEFUL_DRAIN_TIMEOUT":               l.GracefulDrainTimeout.String(),
		"SSE_HEARTBEAT_INTERVAL":               l.SSEHeartbeatInterval.String(),
		"TCP_KEEPALIVE_IDLE":                   l.TCPKeepaliveIdle.String(),
		"TCP_KEEPALIVE_INTERVAL":               l.TCPKeepaliveInterval.String(),
		"TCP_KEEPALIVE_COUNT":                  l.TCPKeepaliveCount,
		"MAX_DISPATCHED_UPSTREAM_ATTEMPTS":     l.MaxDispatchedUpstreamAttempts,
	}
}

func TimeoutLadderStartupLog(l TimeoutLadder) string {
	return fmt.Sprintf(
		"timeout ladder: body=%s queue=%s connect=%s tls=%s first_event=%s precommit=%s stream_idle=%s write_idle=%s lifetime=%s drain=%s sse_hb=%s tcp_idle=%s tcp_intvl=%s tcp_count=%d max_attempts=%d",
		l.RequestBodyNoProgressTimeout,
		l.NewAPIQueueWaitTimeout,
		l.UpstreamConnectTimeout,
		l.UpstreamTLSHandshakeTimeout,
		l.UpstreamAttemptFirstEventTimeout,
		l.PrecommitRecoveryBudget,
		l.UpstreamStreamNoProgressTimeout,
		l.DownstreamWriteNoProgressTimeout,
		l.LogicalRequestMaxLifetime,
		l.GracefulDrainTimeout,
		l.SSEHeartbeatInterval,
		l.TCPKeepaliveIdle,
		l.TCPKeepaliveInterval,
		l.TCPKeepaliveCount,
		l.MaxDispatchedUpstreamAttempts,
	)
}
