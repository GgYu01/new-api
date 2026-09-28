package lifecycle_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/lifecycle"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func sseEvent(json string) string {
	return "data: " + json + "\n\n"
}

func TestCompletionSafeHTTPReplayThroughResponsesAdapter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	t.Setenv("COMPLETION_SAFE", "true")

	failCreated := `{"type":"response.created","response":{"id":"resp_fail"}}`
	okCreated := `{"type":"response.created","response":{"id":"resp_ok"}}`
	okDelta := `{"type":"response.output_text.delta","delta":"hello-safe"}`
	okCompleted := `{"type":"response.completed","response":{"id":"resp_ok","usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`

	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = w.Write([]byte(sseEvent(failCreated)))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		_, _ = w.Write([]byte(sseEvent(okCreated)))
		_, _ = w.Write([]byte(sseEvent(okDelta)))
		_, _ = w.Write([]byte(sseEvent(okCompleted)))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	t.Cleanup(upstream.Close)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4","input":"hi","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("Accept", "text/event-stream")
	lr := lifecycle.New(c.Request.Context(), "root-csafe", true, types.RelayFormatOpenAIResponses)
	lifecycle.Install(c, lr)
	lr.AttachWriter(c)
	t.Cleanup(lr.Cleanup)
	lr.ApplyCompletionSafeEligibility(lifecycle.EligibilityInput{
		Enabled: true,
		Format:  types.RelayFormatOpenAIResponses,
		Path:    "/v1/responses",
	})
	require.True(t, lr.CompletionSafe().Enabled())

	adaptor := &openai.Adaptor{}
	info := &relaycommon.RelayInfo{
		IsStream:       true,
		RelayMode:      relayconstant.RelayModeResponses,
		RequestURLPath: "/v1/responses",
		RelayFormat:    types.RelayFormatOpenAIResponses,
		DisablePing:    true,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:       constant.ChannelTypeOpenAI,
			ChannelBaseUrl:    upstream.URL,
			ApiType:           constant.APITypeOpenAI,
			ApiKey:            "sk-test",
			UpstreamModelName: "gpt-4",
		},
	}
	adaptor.Init(info)

	_, _, err := lr.BeginAttempt()
	require.NoError(t, err)
	resp, err := adaptor.DoRequest(c, info, strings.NewReader(`{"model":"gpt-4","stream":true}`))
	require.NoError(t, err)
	httpResp := resp.(*http.Response)
	_, doErr := adaptor.DoResponse(c, httpResp, info)
	_ = doErr // Responses adapter may return nil on pre-semantic EOF; the gate is downstream leakage.
	require.False(t, lr.SemanticCommitted(), "first attempt must not semantically commit")
	require.NotContains(t, rec.Body.String(), "resp_fail")
	require.NotContains(t, rec.Body.String(), "hello-safe")
	lr.EndAttempt(lifecycle.AttemptRecord{StatusCode: http.StatusBadGateway})
	require.Empty(t, lr.CompletionSafe().Replay(), "failed attempt spool must be discarded")

	_, _, err = lr.BeginAttempt()
	require.NoError(t, err)
	resp, err = adaptor.DoRequest(c, info, strings.NewReader(`{"model":"gpt-4","stream":true}`))
	require.NoError(t, err)
	httpResp = resp.(*http.Response)
	usage, doErr := adaptor.DoResponse(c, httpResp, info)
	require.Nil(t, doErr)
	require.NotNil(t, usage)
	body := rec.Body.String()
	require.NotContains(t, body, "resp_fail", "zero failed-attempt leakage")
	require.Contains(t, body, "resp_ok")
	require.Contains(t, body, "hello-safe")
	require.Contains(t, body, "response.completed")
	require.Contains(t, body, `"total_tokens":5`)
	createdIdx := strings.Index(body, `"type":"response.created"`)
	deltaIdx := strings.Index(body, `"type":"response.output_text.delta"`)
	completedIdx := strings.Index(body, `"type":"response.completed"`)
	require.GreaterOrEqual(t, createdIdx, 0)
	require.Greater(t, deltaIdx, createdIdx)
	require.Greater(t, completedIdx, deltaIdx)
	require.GreaterOrEqual(t, hits.Load(), int32(2))
}

func TestCompletionSafeHTTPEligibilityFallbackClosesResources(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("COMPLETION_SAFE", "true")

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4"}`))
	lr := lifecycle.New(c.Request.Context(), "root-elig", true, types.RelayFormatOpenAIResponses)
	lifecycle.Install(c, lr)
	w := lr.AttachWriter(c)
	t.Cleanup(lr.Cleanup)

	d := lr.ApplyCompletionSafeEligibility(lifecycle.EligibilityInput{
		Enabled:            true,
		Format:             types.RelayFormatOpenAIResponses,
		Path:               "/v1/responses",
		HasSideEffectTools: true,
	})
	require.False(t, d.Eligible)
	require.Equal(t, lifecycle.ReasonSideEffectTools, d.Reason)
	require.False(t, lr.CompletionSafe().Enabled())
	require.Equal(t, lifecycle.FallbackRealtime, lr.CompletionSafe().Fallback())

	_, err := w.WritePayload([]byte(`data: {"type":"response.created","response":{"id":"resp_live"}}\n\n`))
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), "resp_live")

	overflow := lr.ApplyCompletionSafeEligibility(lifecycle.EligibilityInput{
		Enabled:    true,
		Format:     types.RelayFormatOpenAIResponses,
		DiskFull:   true,
		SpoolLimit: 64,
	})
	require.Equal(t, lifecycle.ReasonDiskFull, overflow.Reason)
	require.False(t, lr.SemanticCommitted(), "fallback decided before semantic commit")

	lr.Cleanup()
	require.True(t, lr.CompletionSafe().Closed())
}

func TestCompletionSafeWriterHoldsUntilCompleted(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("COMPLETION_SAFE", "true")
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	lr := lifecycle.New(c.Request.Context(), "root-hold", true, types.RelayFormatOpenAIResponses)
	lifecycle.Install(c, lr)
	w := lr.AttachWriter(c)
	t.Cleanup(lr.Cleanup)
	lr.ApplyCompletionSafeEligibility(lifecycle.EligibilityInput{Enabled: true, Format: types.RelayFormatOpenAIResponses, Path: "/v1/responses"})

	_, err := w.WritePayload([]byte(`data: {"type":"response.created","response":{"id":"resp_ok"}}\n\n`))
	require.NoError(t, err)
	require.NotContains(t, rec.Body.String(), "resp_ok")
	require.False(t, lr.SemanticCommitted())
	require.True(t, lr.KeepaliveOnly() || lr.HeadersCommitted())

	err = w.WriteKeepalive()
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), ": PING")
	require.NotContains(t, rec.Body.String(), "resp_ok")

	_, err = w.WritePayload([]byte(`data: {"type":"response.output_text.delta","delta":"hi"}\n\n`))
	require.NoError(t, err)
	require.NotContains(t, rec.Body.String(), `"delta":"hi"`)

	_, err = w.WritePayload([]byte(`data: {"type":"response.completed","response":{"id":"resp_ok","usage":{"total_tokens":1}}}\n\n`))
	require.NoError(t, err)
	body := rec.Body.String()
	require.Contains(t, body, "resp_ok")
	require.Contains(t, body, `"delta":"hi"`)
	require.Contains(t, body, "response.completed")
	require.True(t, lr.SemanticCommitted())
}
