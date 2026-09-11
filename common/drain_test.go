package common

import (
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDrainHTTPServerWaitsForSlowStream(t *testing.T) {
	var done atomic.Bool
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte("data: done\n\n"))
		done.Store(true)
	})}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	type result struct {
		body string
		err  error
	}
	got := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		if err != nil {
			got <- result{err: err}
			return
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(resp.Body)
		got <- result{body: string(b), err: err}
	}()

	time.Sleep(100 * time.Millisecond)
	require.NoError(t, DrainHTTPServer(srv, 10*time.Second))
	r := <-got
	require.NoError(t, r.err)
	require.Equal(t, "data: done\n\n", r.body)
	require.True(t, done.Load(), "slow handler must complete during drain")
}
