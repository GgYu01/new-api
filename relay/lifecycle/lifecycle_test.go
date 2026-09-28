package lifecycle

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func Test_LogicalRequest_rootContextSurvivesAttemptCancel(t *testing.T) {
	root, cancel := context.WithCancel(context.Background())
	defer cancel()
	lr := New(root, "root-1", true, types.RelayFormatOpenAI)
	defer lr.Cleanup()

	id, attemptCtx, err := lr.BeginAttempt()
	require.NoError(t, err)
	require.NotEmpty(t, id)
	require.NotEqual(t, lr.RootContext(), attemptCtx)

	lr.EndAttempt(AttemptRecord{CancelCause: CancelCauseAttemptEnd})
	require.NoError(t, lr.RootContext().Err())
	require.Error(t, attemptCtx.Err())
}

func Test_Writer_pingIsKeepaliveNotSemantic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	lr := New(c.Request.Context(), "root-ping", true, types.RelayFormatOpenAI)
	defer lr.Cleanup()
	Install(c, lr)
	w := lr.AttachWriter(c)

	require.NoError(t, w.WriteKeepalive())
	require.Contains(t, rec.Body.String(), ": PING")
	require.True(t, lr.HeadersCommitted())
	require.True(t, lr.KeepaliveOnly())
	require.False(t, lr.SemanticCommitted())
}

func Test_Writer_textIsSemantic(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	lr := New(c.Request.Context(), "root-text", true, types.RelayFormatOpenAI)
	defer lr.Cleanup()
	Install(c, lr)
	w := lr.AttachWriter(c)

	payload := []byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	_, err := w.WritePayload(payload)
	require.NoError(t, err)
	require.True(t, lr.SemanticCommitted())
	require.False(t, lr.KeepaliveOnly())
}

func Test_ShouldHoldUpstreamEvent_responseCreated(t *testing.T) {
	require.True(t, ShouldHoldUpstreamEvent(`{"id":"resp_failed","object":"chat.completion.chunk","choices":[]}`))
	require.True(t, ShouldHoldUpstreamEvent(`{"type":"response.created","response":{"id":"resp_1"}}`))
	require.False(t, ShouldHoldUpstreamEvent(`{"id":"chatcmpl-1","choices":[{"delta":{"content":"hi"}}]}`))
}

func Test_CanRecover_presemantic503(t *testing.T) {
	lr := New(context.Background(), "root-503", true, types.RelayFormatOpenAI)
	defer lr.Cleanup()
	_, _, err := lr.BeginAttempt()
	require.NoError(t, err)
	lr.EndAttempt(AttemptRecord{StatusCode: 503})

	apiErr := types.NewErrorWithStatusCode(io.EOF, types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	ok, class := CanRecover(lr, apiErr, 7)
	require.True(t, ok)
	require.NotEqual(t, ClassSemanticCommitted, class)
}

func Test_CanRecover_blocksAfterSemantic(t *testing.T) {
	lr := New(context.Background(), "root-sem", true, types.RelayFormatOpenAI)
	defer lr.Cleanup()
	lr.MarkSemanticCommitted()
	apiErr := types.NewErrorWithStatusCode(io.EOF, types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	ok, class := CanRecover(lr, apiErr, 7)
	require.False(t, ok)
	require.Equal(t, ClassSemanticCommitted, class)
}

func Test_CanRecover_blocksSideEffectUnknown(t *testing.T) {
	lr := New(context.Background(), "root-tool", true, types.RelayFormatOpenAI)
	defer lr.Cleanup()
	lr.ObserveRequest(&dto.GeneralOpenAIRequest{Tools: []dto.ToolCallRequest{{}}})
	require.True(t, lr.HasSideEffects())
	lr.MarkUpstreamMayHaveExecuted()
	apiErr := types.NewErrorWithStatusCode(io.EOF, types.ErrorCodeDoRequestFailed, http.StatusBadGateway)
	ok, class := CanRecover(lr, apiErr, 7)
	require.False(t, ok)
	require.Equal(t, ClassSideEffectUnknown, class)
}

func Test_WriteFailure_openaiSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	lr := New(c.Request.Context(), "root-fail", true, types.RelayFormatOpenAI)
	defer lr.Cleanup()
	Install(c, lr)
	lr.AttachWriter(c)
	require.NoError(t, lr.Writer().WriteKeepalive())

	apiErr := types.NewErrorWithStatusCode(io.EOF, types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	require.NoError(t, WriteFailure(c, lr, apiErr))
	body := rec.Body.String()
	require.Contains(t, body, ": PING")
	require.Contains(t, body, "data: ")
	require.Contains(t, body, "[DONE]")
	require.True(t, lr.TerminalSent())
	require.Equal(t, http.StatusOK, rec.Code, "headers-committed failure must not rewrite status")
}

func Test_Heartbeat_spansAttemptsWithoutSleep(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		cfg := DefaultConfig()
		cfg.SSEHeartbeatInterval = 10 * time.Second
		restore := SetTestConfig(cfg)
		defer restore()

		lr := New(c.Request.Context(), "root-hb", true, types.RelayFormatOpenAI)
		defer lr.Cleanup()
		Install(c, lr)
		lr.AttachWriter(c)
		lr.StartHeartbeat(c, 10*time.Second)

		time.Sleep(10 * time.Second)
		synctest.Wait()
		require.Contains(t, rec.Body.String(), ": PING")
		require.False(t, lr.SemanticCommitted())
	})
}

func Test_HandleNon2xx_doesNotCommit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	lr := New(c.Request.Context(), "root-n2xx", true, types.RelayFormatOpenAI)
	defer lr.Cleanup()
	Install(c, lr)

	resp := &http.Response{
		StatusCode: http.StatusServiceUnavailable,
		Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"busy"}}`)),
		Header:     make(http.Header),
	}
	apiErr := HandleNon2xx(c, resp, false)
	require.NotNil(t, apiErr)
	require.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	require.False(t, lr.SemanticCommitted())
	require.False(t, rec.Body.Len() > 0)
}
