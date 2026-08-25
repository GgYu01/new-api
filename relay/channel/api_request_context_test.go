package channel

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	taskdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type contextAPIAdaptor struct {
	Adaptor
	requestURL string
}

func (a *contextAPIAdaptor) GetRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	return a.requestURL, nil
}

func (a *contextAPIAdaptor) SetupRequestHeader(_ *gin.Context, _ *http.Header, _ *relaycommon.RelayInfo) error {
	return nil
}

type contextTaskAdaptor struct {
	TaskAdaptor
	requestURL string
}

func (a *contextTaskAdaptor) BuildRequestURL(_ *relaycommon.RelayInfo) (string, error) {
	return a.requestURL, nil
}

func (a *contextTaskAdaptor) BuildRequestHeader(_ *gin.Context, _ *http.Request, _ *relaycommon.RelayInfo) error {
	return nil
}

func (a *contextTaskAdaptor) ValidateRequestAndSetAction(_ *gin.Context, _ *relaycommon.RelayInfo) *taskdto.TaskError {
	return nil
}

func (a *contextTaskAdaptor) AdjustBillingOnComplete(_ *model.Task, _ *relaycommon.TaskInfo) int {
	return 0
}

type upstreamRequestResult struct {
	response *http.Response
	err      error
}

type socketCloseProbe struct {
	server         *httptest.Server
	requestStarted chan struct{}
	socketClosed   chan struct{}
	release        chan struct{}
}

func newSocketCloseProbe() *socketCloseProbe {
	probe := &socketCloseProbe{
		requestStarted: make(chan struct{}),
		socketClosed:   make(chan struct{}),
		release:        make(chan struct{}),
	}
	probe.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		_ = r.Body.Close()

		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			close(probe.requestStarted)
			return
		}
		defer conn.Close()
		close(probe.requestStarted)

		readDone := make(chan struct{})
		go func() {
			var nextByte [1]byte
			_, _ = conn.Read(nextByte[:])
			close(readDone)
		}()

		select {
		case <-readDone:
			close(probe.socketClosed)
		case <-probe.release:
			_ = conn.Close()
			<-readDone
		}
	}))
	return probe
}

func (p *socketCloseProbe) Close() {
	close(p.release)
	p.server.Close()
}

func testUpstreamCancellation(t *testing.T, invoke func(*gin.Context) (*http.Response, error)) {
	t.Helper()

	probe := newSocketCloseProbe()
	defer probe.Close()

	downstreamContext, cancelDownstream := context.WithCancel(context.Background())
	defer cancelDownstream()
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodPost, probe.server.URL, bytes.NewReader([]byte("request body"))).WithContext(downstreamContext)

	resultCh := make(chan upstreamRequestResult, 1)
	go func() {
		response, err := invoke(ginContext)
		resultCh <- upstreamRequestResult{response: response, err: err}
	}()

	select {
	case <-probe.requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("upstream request did not start")
	}

	cancelDownstream()

	select {
	case <-probe.socketClosed:
	case <-time.After(time.Second):
		t.Fatal("downstream cancellation did not close the upstream socket")
	}

	select {
	case result := <-resultCh:
		if result.response != nil {
			require.NoError(t, result.response.Body.Close())
		}
		require.Error(t, result.err)
		require.ErrorIs(t, result.err, context.Canceled)
		var relayErr *relaytypes.NewAPIError
		require.ErrorAs(t, result.err, &relayErr)
		require.True(t, relaytypes.IsSkipRetryError(relayErr), "downstream cancellation must never retry another channel")
		require.False(t, relaytypes.IsRecordErrorLog(relayErr), "downstream cancellation is an informational lifecycle event, not an upstream failure")
	case <-time.After(2 * time.Second):
		t.Fatal("relay request did not return after cancellation")
	}
}

func TestDownstreamAwareConnCancelsSocketAndClosesOnce(t *testing.T) {
	downstreamContext, cancelDownstream := context.WithCancel(context.Background())
	upstream, peer := net.Pipe()
	defer peer.Close()

	conn := newDownstreamAwareConn(downstreamContext, upstream)
	peerRead := make(chan error, 1)
	go func() {
		var nextByte [1]byte
		_, err := peer.Read(nextByte[:])
		peerRead <- err
	}()
	cancelDownstream()

	select {
	case err := <-peerRead:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("context callback did not close the upstream connection")
	}

	require.NoError(t, conn.Close(), "closing an already canceled connection must be idempotent")
	_, err := conn.Write([]byte("after close"))
	require.Error(t, err)
}

func TestDownstreamAwareConnTenThousandCancelCyclesLeaveNoWatchers(t *testing.T) {
	runtime.GC()
	baseline := runtime.NumGoroutine()

	for range 10_000 {
		downstreamContext, cancelDownstream := context.WithCancel(context.Background())
		upstream, peer := net.Pipe()
		conn := newDownstreamAwareConn(downstreamContext, upstream)
		cancelDownstream()
		require.NoError(t, conn.Close())
		require.NoError(t, peer.Close())
	}

	require.Eventually(t, func() bool {
		runtime.GC()
		return runtime.NumGoroutine() <= baseline+4
	}, 2*time.Second, 10*time.Millisecond, "context callbacks must not accumulate watcher goroutines")
}

func TestDoApiRequestPropagatesDownstreamCancellation(t *testing.T) {
	service.InitHttpClient()
	gin.SetMode(gin.TestMode)

	testUpstreamCancellation(t, func(c *gin.Context) (*http.Response, error) {
		adaptor := &contextAPIAdaptor{requestURL: c.Request.URL.String()}
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
		return DoApiRequest(adaptor, c, info, bytes.NewReader([]byte("request body")))
	})
}

func TestDoFormRequestPropagatesDownstreamCancellation(t *testing.T) {
	service.InitHttpClient()
	gin.SetMode(gin.TestMode)

	testUpstreamCancellation(t, func(c *gin.Context) (*http.Response, error) {
		c.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		adaptor := &contextAPIAdaptor{requestURL: c.Request.URL.String()}
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
		return DoFormRequest(adaptor, c, info, bytes.NewBufferString("prompt=hello"))
	})
}

func TestDoTaskApiRequestPropagatesDownstreamCancellation(t *testing.T) {
	service.InitHttpClient()
	gin.SetMode(gin.TestMode)

	testUpstreamCancellation(t, func(c *gin.Context) (*http.Response, error) {
		adaptor := &contextTaskAdaptor{requestURL: c.Request.URL.String()}
		info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
		return DoTaskApiRequest(adaptor, c, info, io.NopCloser(bytes.NewReader([]byte("request body"))))
	})
}

func TestDoWssRequestPropagatesDownstreamCancellationDuringDial(t *testing.T) {
	gin.SetMode(gin.TestMode)

	probe := newSocketCloseProbe()
	defer probe.Close()

	downstreamContext, cancelDownstream := context.WithCancel(context.Background())
	defer cancelDownstream()
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/v1/realtime", nil).WithContext(downstreamContext)

	wsURL := "ws" + strings.TrimPrefix(probe.server.URL, "http")
	adaptor := &contextAPIAdaptor{requestURL: wsURL}
	info := &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}}
	resultCh := make(chan error, 1)
	go func() {
		conn, err := DoWssRequest(adaptor, ginContext, info, nil)
		if conn != nil {
			_ = conn.Close()
		}
		resultCh <- err
	}()

	select {
	case <-probe.requestStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("websocket upstream request did not start")
	}

	cancelDownstream()

	select {
	case <-probe.socketClosed:
	case <-time.After(time.Second):
		t.Fatal("downstream cancellation did not close the websocket dial socket")
	}

	select {
	case err := <-resultCh:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
		var relayErr *relaytypes.NewAPIError
		require.ErrorAs(t, err, &relayErr)
		require.True(t, relaytypes.IsSkipRetryError(relayErr), "websocket cancellation must never retry another channel")
		require.False(t, relaytypes.IsRecordErrorLog(relayErr), "websocket cancellation is an informational lifecycle event, not an upstream failure")
	case <-time.After(2 * time.Second):
		t.Fatal("websocket dial did not return after cancellation")
	}
}
