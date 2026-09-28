package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newPresemantic503Fixture(t *testing.T) (*gin.Context, *httptest.ResponseRecorder, *relaycommon.RelayInfo, *types.NewAPIError) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	info := &relaycommon.RelayInfo{}
	upstreamErr := types.NewErrorWithStatusCode(
		fmt.Errorf("upstream service unavailable"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusServiceUnavailable,
	)
	return c, recorder, info, upstreamErr
}

func Test_shouldRetry_presemanticPingDoesNotBlockRecoverable503(t *testing.T) {
	t.Run("retries_before_any_byte_output", func(t *testing.T) {
		c, recorder, info, upstreamErr := newPresemantic503Fixture(t)

		require.False(t, c.Writer.Written())
		require.Empty(t, recorder.Body.Bytes())
		require.True(t, shouldRetry(c, info, upstreamErr, 1),
			"recoverable HTTP 503 must retry before any downstream byte is written")
	})

	t.Run("retries_after_real_keepalive_ping_flush", func(t *testing.T) {
		c, recorder, info, upstreamErr := newPresemantic503Fixture(t)
		helper.SetEventStreamHeaders(c)

		// Production pinger is startPingKeepAlive/sendPingData in
		// relay/channel/api_request.go; sendPingData is unexported. Drive the
		// same writer it uses: ExtendWriteDeadline + helper.PingData.
		helper.ExtendWriteDeadline(c)
		require.NoError(t, helper.PingData(c))
		require.Contains(t, recorder.Body.String(), ": PING\n\n")
		require.True(t, c.Writer.Written(), "PING+flush commits gin.Writer.Written")

		require.True(t, shouldRetry(c, info, upstreamErr, 1),
			"SSE comment heartbeat must not prohibit recovery from presemantic HTTP 503")
	})

	t.Run("does_not_retry_after_semantic_text_output", func(t *testing.T) {
		c, recorder, info, upstreamErr := newPresemantic503Fixture(t)

		require.NoError(t, helper.StringData(c, `{"id":"chatcmpl-1","choices":[{"delta":{"content":"hello"}}]}`))
		require.Contains(t, recorder.Body.String(), "data: ")
		require.Contains(t, recorder.Body.String(), `"content":"hello"`)
		require.True(t, c.Writer.Written())

		require.False(t, shouldRetry(c, info, upstreamErr, 1),
			"semantic text output must keep the existing no-retry guard")
	})
}
