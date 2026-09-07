package common

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func stallTestHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, err := io.Copy(io.Discard, r.Body)
		if err != nil {
			if IsRequestBodyStalledError(err) {
				w.WriteHeader(http.StatusRequestTimeout)
				_, _ = w.Write([]byte("stalled"))
				return
			}
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		_, _ = w.Write([]byte(fmt.Sprintf("len=%d", n)))
	})
}

// TestStallGuardRootReturns408OnStalledUpload reproduces the half-open upload
// seen from the Sep 2026 customer relay: the request declares a large body,
// sends a prefix, then goes quiet. The guard must fail the read with 408 well
// before the client's own 60s timeout, instead of pinning the worker.
func TestStallGuardRootReturns408OnStalledUpload(t *testing.T) {
	t.Setenv("BODY_STALL_WINDOW", "300ms")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: StallGuardRoot(stallTestHandler())}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	req := "POST /v1/responses HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: 100000\r\n\r\n{\"partial\":"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no response while body stalled: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("stall guard returned too late: %s", elapsed)
	}
	if !strings.Contains(string(buf[:n]), "408") {
		t.Fatalf("expected 408, got: %q", string(buf[:n]))
	}
}

func TestStallGuardRootPassesFullBody(t *testing.T) {
	t.Setenv("BODY_STALL_WINDOW", "300ms")

	srv := httptest.NewServer(StallGuardRoot(stallTestHandler()))
	t.Cleanup(srv.Close)

	body := strings.Repeat("x", 65536)
	resp, err := http.Post(srv.URL+"/", "text/plain", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body = %q", resp.StatusCode, string(data))
	}
	if want := fmt.Sprintf("len=%d", len(body)); string(data) != want {
		t.Fatalf("body mismatch: got %q, want %q", string(data), want)
	}
}

func TestRequestBodyStallWindowParsing(t *testing.T) {
	t.Setenv("BODY_STALL_WINDOW", "")
	if got := RequestBodyStallWindow(); got != 10*time.Second {
		t.Fatalf("default window = %s, want 10s", got)
	}
	t.Setenv("BODY_STALL_WINDOW", "0")
	if got := RequestBodyStallWindow(); got != 0 {
		t.Fatalf("disabled window = %s, want 0", got)
	}
	t.Setenv("BODY_STALL_WINDOW", "30s")
	if got := RequestBodyStallWindow(); got != 30*time.Second {
		t.Fatalf("explicit window = %s, want 30s", got)
	}
	t.Setenv("BODY_STALL_WINDOW", "garbage")
	if got := RequestBodyStallWindow(); got != 10*time.Second {
		t.Fatalf("invalid window = %s, want fallback 10s", got)
	}
}

func TestIsRequestBodyStalledError(t *testing.T) {
	if IsRequestBodyStalledError(nil) {
		t.Fatal("nil must not match")
	}
	if IsRequestBodyStalledError(ErrRequestBodyTooLarge) {
		t.Fatal("too-large must not match")
	}
	wrapped := fmt.Errorf("disk storage creation failed: %w", ErrRequestBodyStalled)
	if !IsRequestBodyStalledError(wrapped) {
		t.Fatal("wrapped stall must match")
	}
}

// TestStallGuardRootBoundsDrainAfterEarlyAbort reproduces the half-open
// client + early-abort case seen in production: the handler rejects the
// request (401) without consuming the declared body, the client keeps the
// socket open with the rest of the body unsent, and net/http drains the
// unread remainder through its own transport reference before flushing the
// response. The guard must bound that drain so the client learns the result
// quickly instead of waiting for its own timeout.
func TestStallGuardRootBoundsDrainAfterEarlyAbort(t *testing.T) {
	t.Setenv("BODY_STALL_WINDOW", "300ms")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	reject := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("unauthorized"))
	})
	srv := &http.Server{Handler: StallGuardRoot(reject)}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	conn, err := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	req := "POST /v1/chat/completions HTTP/1.1\r\nHost: test\r\nContent-Type: application/json\r\nContent-Length: 100000\r\n\r\n{\"partial\":"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	if err := conn.SetReadDeadline(time.Now().Add(6 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 2048)
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("no response while drain blocked: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("early abort flushed too late: %s", elapsed)
	}
	if !strings.Contains(string(buf[:n]), "401") {
		t.Fatalf("expected 401, got: %q", string(buf[:n]))
	}
}
