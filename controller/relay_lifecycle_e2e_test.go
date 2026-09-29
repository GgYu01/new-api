package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/lifecycle"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayLifecycleE2E_SemanticCommittedBlocksRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))

	rootID := "req-test-e2e-1"
	c.Set(common.RequestIdKey, rootID)
	lr := lifecycle.New(c.Request.Context(), rootID, true, types.RelayFormatOpenAI)
	lifecycle.Install(c, lr)
	lr.AttachWriter(c)
	defer lr.Cleanup()

	// 1. Begin attempt #1
	attemptID1, attemptCtx1, err := lr.BeginAttempt()
	require.NoError(t, err)
	assert.Equal(t, "req-test-e2e-1-a0", attemptID1)
	assert.NotNil(t, attemptCtx1)

	// Simulate semantic commit during attempt #1 (e.g. first token chunk written)
	lr.MarkSemanticCommitted()
	assert.True(t, lr.SemanticCommitted())

	// Attempt #1 fails with upstream 502/503
	lr.EndAttempt(lifecycle.AttemptRecord{
		ID:                 attemptID1,
		Index:              0,
		ChannelID:          1,
		StatusCode:         http.StatusBadGateway,
		UpstreamMayExecute: true,
	})

	apiErr := types.NewErrorWithStatusCode(fmt.Errorf("upstream bad gateway"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)

	// 2. DecideRelayRetry MUST stop due to semantic commit!
	decision := service.DecideRelayRetry(c, apiErr, 2)
	assert.Equal(t, "stop", decision.Action)
	assert.Equal(t, lifecycle.ClassSemanticCommitted, decision.Reason)
	assert.Equal(t, "lifecycle", decision.Source)

	// shouldRetry MUST also return false
	info := &relaycommon.RelayInfo{}
	assert.False(t, shouldRetry(c, info, apiErr, 2))

	// BeginAttempt for a second try MUST fail because semantic output was committed
	_, _, err = lr.BeginAttempt()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "semantic output already committed")
}

func TestRelayLifecycleE2E_UnknownSideEffectsBlockRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))

	rootID := "req-test-e2e-2"
	c.Set(common.RequestIdKey, rootID)
	lr := lifecycle.New(c.Request.Context(), rootID, false, types.RelayFormatOpenAI)
	lifecycle.Install(c, lr)
	defer lr.Cleanup()

	// Mark request as having side-effecting tools
	lr.MarkHasSideEffects()
	assert.True(t, lr.HasSideEffects())

	// Begin attempt #1
	attemptID1, _, err := lr.BeginAttempt()
	require.NoError(t, err)

	// Attempt #1 may have executed upstream (e.g. timeout or post-dispatch failure)
	lr.EndAttempt(lifecycle.AttemptRecord{
		ID:                 attemptID1,
		Index:              0,
		ChannelID:          1,
		StatusCode:         http.StatusGatewayTimeout,
		UpstreamMayExecute: true,
	})

	apiErr := types.NewErrorWithStatusCode(fmt.Errorf("upstream timeout"), types.ErrorCodeBadResponseStatusCode, http.StatusGatewayTimeout)

	// DecideRelayRetry MUST stop due to unknown side effect!
	decision := service.DecideRelayRetry(c, apiErr, 2)
	assert.Equal(t, "stop", decision.Action)
	assert.Equal(t, lifecycle.ClassSideEffectUnknown, decision.Reason)
	assert.Equal(t, "lifecycle", decision.Source)

	assert.False(t, shouldRetry(c, &relaycommon.RelayInfo{}, apiErr, 2))
}

func TestRelayLifecycleE2E_PresemanticTransportErrorAllowsRetry(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"gpt-4"}`))

	rootID := "req-test-e2e-3"
	c.Set(common.RequestIdKey, rootID)
	lr := lifecycle.New(c.Request.Context(), rootID, false, types.RelayFormatOpenAI)
	lifecycle.Install(c, lr)
	defer lr.Cleanup()

	// Attempt #1 fails before semantic commit and without side effects
	attemptID1, _, err := lr.BeginAttempt()
	require.NoError(t, err)

	lr.EndAttempt(lifecycle.AttemptRecord{
		ID:                 attemptID1,
		Index:              0,
		ChannelID:          1,
		StatusCode:         http.StatusServiceUnavailable,
		UpstreamMayExecute: false,
	})

	apiErr := types.NewErrorWithStatusCode(fmt.Errorf("service unavailable"), types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)

	decision := service.DecideRelayRetry(c, apiErr, 2)
	assert.Equal(t, "retry", decision.Action)
	assert.True(t, shouldRetry(c, &relaycommon.RelayInfo{}, apiErr, 2))

	// Attempt #2 can begin normally
	attemptID2, _, err := lr.BeginAttempt()
	require.NoError(t, err)
	assert.Equal(t, "req-test-e2e-3-a1", attemptID2)
	lr.EndAttempt(lifecycle.AttemptRecord{ID: attemptID2, Index: 1})
}
