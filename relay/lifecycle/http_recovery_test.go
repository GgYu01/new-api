package lifecycle_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	"github.com/QuantumNous/new-api/relay/channel/sub2api"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relay/imagebridge"
	"github.com/QuantumNous/new-api/relay/lifecycle"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func init() {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
}

func newRelayContext(t *testing.T, path string, stream bool, format types.RelayFormat) (*gin.Context, *httptest.ResponseRecorder, *lifecycle.LogicalRequest) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"gpt-4","stream":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	if stream {
		c.Request.Header.Set("Accept", "text/event-stream")
	}
	lr := lifecycle.New(c.Request.Context(), "root-http", stream, format)
	lifecycle.Install(c, lr)
	lr.AttachWriter(c)
	t.Cleanup(lr.Cleanup)
	return c, rec, lr
}

func newInfo(baseURL, path string, stream bool) *relaycommon.RelayInfo {
	info := &relaycommon.RelayInfo{
		IsStream:       stream,
		RelayMode:      relayconstant.RelayModeChatCompletions,
		RequestURLPath: path,
		RelayFormat:    types.RelayFormatOpenAI,
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelType:    constant.ChannelTypeOpenAI,
			ChannelBaseUrl: baseURL,
			ApiType:        constant.APITypeOpenAI,
			ApiKey:         "sk-test",
		},
	}
	return info
}

func Test_OpenAIAdaptor_presemantic503ThenSuccess(t *testing.T) {
	cfg := lifecycle.DefaultConfig()
	cfg.SSEHeartbeatInterval = 10 * time.Millisecond
	restore := lifecycle.SetTestConfig(cfg)
	defer restore()

	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"busy"}}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-ok\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	adaptor := &openai.Adaptor{}
	c, rec, lr := newRelayContext(t, "/v1/chat/completions", true, types.RelayFormatOpenAI)
	info := newInfo(upstream.URL, "/v1/chat/completions", true)
	adaptor.Init(info)

	_, attemptCtx, err := lr.BeginAttempt()
	require.NoError(t, err)
	_ = attemptCtx
	resp, err := adaptor.DoRequest(c, info, strings.NewReader(`{"model":"gpt-4","stream":true}`))
	require.NoError(t, err)
	httpResp := resp.(*http.Response)
	require.Equal(t, http.StatusServiceUnavailable, httpResp.StatusCode)
	apiErr := lifecycle.HandleNon2xx(c, httpResp, false)
	require.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	require.False(t, lr.SemanticCommitted())
	require.True(t, shouldKeepRetry(t, lr, apiErr))
	lr.EndAttempt(lifecycle.AttemptRecord{StatusCode: 503})

	_, _, err = lr.BeginAttempt()
	require.NoError(t, err)
	resp, err = adaptor.DoRequest(c, info, strings.NewReader(`{"model":"gpt-4","stream":true}`))
	require.NoError(t, err)
	httpResp = resp.(*http.Response)
	require.Equal(t, http.StatusOK, httpResp.StatusCode)
	usage, doErr := adaptor.DoResponse(c, httpResp, info)
	require.Nil(t, doErr)
	require.NotNil(t, usage)
	require.True(t, lr.SemanticCommitted())
	require.Contains(t, rec.Body.String(), "hi")
	require.GreaterOrEqual(t, hits.Load(), int32(2))
}

func Test_OpenAIAdaptor_presemantic200SSEDisconnectRecovers(t *testing.T) {
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			_, _ = w.Write([]byte("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_fail\"}}\n\n"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		_, _ = w.Write([]byte("data: {\"id\":\"chatcmpl-ok\",\"choices\":[{\"delta\":{\"content\":\"recovered\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer upstream.Close()

	adaptor := &openai.Adaptor{}
	c, rec, lr := newRelayContext(t, "/v1/chat/completions", true, types.RelayFormatOpenAI)
	info := newInfo(upstream.URL, "/v1/chat/completions", true)
	adaptor.Init(info)

	_, _, err := lr.BeginAttempt()
	require.NoError(t, err)
	resp, err := adaptor.DoRequest(c, info, strings.NewReader(`{"model":"gpt-4","stream":true}`))
	require.NoError(t, err)
	httpResp := resp.(*http.Response)
	_, doErr := adaptor.DoResponse(c, httpResp, info)
	require.NotNil(t, doErr)
	require.False(t, lr.SemanticCommitted())
	require.NotContains(t, rec.Body.String(), "resp_fail")
	lr.EndAttempt(lifecycle.AttemptRecord{StatusCode: doErr.StatusCode})
	lr.ResetHold()

	_, _, err = lr.BeginAttempt()
	require.NoError(t, err)
	resp, err = adaptor.DoRequest(c, info, strings.NewReader(`{"model":"gpt-4","stream":true}`))
	require.NoError(t, err)
	httpResp = resp.(*http.Response)
	_, doErr = adaptor.DoResponse(c, httpResp, info)
	require.Nil(t, doErr)
	require.Contains(t, rec.Body.String(), "recovered")
	require.NotContains(t, rec.Body.String(), "resp_fail")
}

func Test_Sub2APIAdaptor_passthroughPresemantic503(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/v1/chat/completions", r.URL.Path)
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"passthrough busy"}}`)
	}))
	defer upstream.Close()

	adaptor := &sub2api.Adaptor{}
	c, _, lr := newRelayContext(t, "/v1/chat/completions", true, types.RelayFormatOpenAI)
	info := newInfo(upstream.URL, "/v1/chat/completions", true)
	info.ChannelMeta.ChannelType = constant.ChannelTypeSub2API
	info.ChannelMeta.ApiType = constant.APITypeSub2API
	adaptor.Init(info)

	_, _, err := lr.BeginAttempt()
	require.NoError(t, err)
	resp, err := adaptor.DoRequest(c, info, strings.NewReader(`{"model":"gpt-4"}`))
	require.NoError(t, err)
	httpResp := resp.(*http.Response)
	apiErr := lifecycle.HandleNon2xx(c, httpResp, false)
	require.Equal(t, http.StatusServiceUnavailable, apiErr.StatusCode)
	require.False(t, lr.SemanticCommitted())
	ok, _ := lifecycle.CanRecover(lr, apiErr, 7)
	require.True(t, ok)
}

func Test_ImageBridgeEnvelope_failureAfterPing(t *testing.T) {
	c, rec, lr := newRelayContext(t, "/v1/responses", true, types.RelayFormatOpenAIResponses)
	imagebridge.SetContext(c, imagebridge.Intent{Envelope: imagebridge.EnvelopeResponses, Stream: true, ClientModel: "gpt-image-2"})
	require.NoError(t, helper.PingData(c))
	require.Contains(t, rec.Body.String(), ": PING")
	require.False(t, lr.SemanticCommitted())

	apiErr := types.NewErrorWithStatusCode(io.EOF, types.ErrorCodeBadResponseStatusCode, http.StatusServiceUnavailable)
	require.NoError(t, lifecycle.WriteFailure(c, lr, apiErr))
	body := rec.Body.String()
	require.Contains(t, body, "response.failed")
	require.NotContains(t, body, `"status":"completed"`)
	require.Equal(t, http.StatusOK, rec.Code)
}

func Test_NonSSE_jsonHasNoPing(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	adaptor := &openai.Adaptor{}
	c, rec, lr := newRelayContext(t, "/v1/chat/completions", false, types.RelayFormatOpenAI)
	info := newInfo(upstream.URL, "/v1/chat/completions", false)
	info.IsStream = false
	adaptor.Init(info)
	_, _, err := lr.BeginAttempt()
	require.NoError(t, err)
	resp, err := adaptor.DoRequest(c, info, strings.NewReader(`{"model":"gpt-4"}`))
	require.NoError(t, err)
	httpResp := resp.(*http.Response)
	_, doErr := adaptor.DoResponse(c, httpResp, info)
	require.Nil(t, doErr)
	require.NotContains(t, rec.Body.String(), ": PING")
}

func shouldKeepRetry(t *testing.T, lr *lifecycle.LogicalRequest, apiErr *types.NewAPIError) bool {
	t.Helper()
	ok, _ := lifecycle.CanRecover(lr, apiErr, 7)
	return ok
}

var _ channel.Adaptor = (*openai.Adaptor)(nil)
var _ channel.Adaptor = (*sub2api.Adaptor)(nil)
