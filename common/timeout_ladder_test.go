package common

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_DefaultTimeoutLadder_matchesR13Contract(t *testing.T) {
	ladder := DefaultTimeoutLadder()
	assert.Equal(t, 10*time.Minute, ladder.RequestBodyNoProgressTimeout)
	assert.Equal(t, 10*time.Minute, ladder.NewAPIQueueWaitTimeout)
	assert.Equal(t, 15*time.Second, ladder.UpstreamConnectTimeout)
	assert.Equal(t, 30*time.Second, ladder.UpstreamTLSHandshakeTimeout)
	assert.Equal(t, 10*time.Minute, ladder.UpstreamAttemptFirstEventTimeout)
	assert.Equal(t, 30*time.Minute, ladder.PrecommitRecoveryBudget)
	assert.Equal(t, 10*time.Minute, ladder.UpstreamStreamNoProgressTimeout)
	assert.Equal(t, 10*time.Minute, ladder.DownstreamWriteNoProgressTimeout)
	assert.Equal(t, 60*time.Minute, ladder.LogicalRequestMaxLifetime)
	assert.Equal(t, 60*time.Minute, ladder.GracefulDrainTimeout)
	assert.Equal(t, 15*time.Second, ladder.SSEHeartbeatInterval)
	assert.Equal(t, 60*time.Second, ladder.TCPKeepaliveIdle)
	assert.Equal(t, 20*time.Second, ladder.TCPKeepaliveInterval)
	assert.Equal(t, 3, ladder.TCPKeepaliveCount)
	assert.Equal(t, 8, ladder.MaxDispatchedUpstreamAttempts)
}

func Test_LoadTimeoutLadder_invalidEnvKeepsDefault(t *testing.T) {
	t.Setenv("DOWNSTREAM_WRITE_NO_PROGRESS_TIMEOUT", "not-a-duration")
	t.Setenv("TCP_KEEPALIVE_COUNT", "-1")
	ladder := LoadTimeoutLadder()
	assert.Equal(t, DefaultDownstreamWriteNoProgressTimeout, ladder.DownstreamWriteNoProgressTimeout)
	assert.Equal(t, DefaultTCPKeepaliveCount, ladder.TCPKeepaliveCount)
}

func Test_LoadTimeoutLadder_overridesFromEnv(t *testing.T) {
	t.Setenv("DOWNSTREAM_WRITE_NO_PROGRESS_TIMEOUT", "9m59s")
	t.Setenv("UPSTREAM_CONNECT_TIMEOUT", "12s")
	ladder := LoadTimeoutLadder()
	assert.Equal(t, 9*time.Minute+59*time.Second, ladder.DownstreamWriteNoProgressTimeout)
	assert.Equal(t, 12*time.Second, ladder.UpstreamConnectTimeout)
}

func Test_AttemptFirstEventDeadline_clipsToRootRemaining(t *testing.T) {
	ladder := DefaultTimeoutLadder()
	assert.Equal(t, 4*time.Minute, ladder.AttemptFirstEventDeadline(4*time.Minute))
	assert.Equal(t, 10*time.Minute, ladder.AttemptFirstEventDeadline(40*time.Minute))
}

func Test_TimeoutLadderStartupLog_containsStageNames(t *testing.T) {
	line := TimeoutLadderStartupLog(DefaultTimeoutLadder())
	require.Contains(t, line, "write_idle=10m0s")
	require.Contains(t, line, "first_event=10m0s")
	require.Contains(t, line, "max_attempts=8")
	require.NotContains(t, line, "90s")
	require.NotContains(t, line, "240s")
}
