package common

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrRequestBodyStalled reports an inbound client upload that stopped sending
// data for longer than RequestBodyStallWindow. Without the guard such
// half-open bodies pin a relay worker until the client's own timeout finally
// aborts the connection.
var ErrRequestBodyStalled = errors.New("request body stalled")

func IsRequestBodyStalledError(err error) bool {
	return errors.Is(err, ErrRequestBodyStalled)
}

// RequestBodyStallWindow returns the no-progress window applied to inbound
// POST bodies. Override with BODY_STALL_WINDOW (Go duration, e.g. "30s");
// "0" disables the guard. Default 10s: generous for slow-but-alive uploads,
// an order of magnitude tighter than the 60s client timeouts observed
// stalling the image bridge with half-open uploads.
func RequestBodyStallWindow() time.Duration {
	raw := strings.TrimSpace(os.Getenv("BODY_STALL_WINDOW"))
	if raw == "" {
		return 10 * time.Second
	}
	window, err := time.ParseDuration(raw)
	if err != nil || window < 0 {
		return 10 * time.Second
	}
	return window
}

// StallGuardRoot wraps the top-level HTTP handler so every inbound POST body
// read enforces a no-progress deadline. It operates on the raw server
// ResponseWriter (before gin wraps it) because http.ResponseController can
// only drive read deadlines on the transport's own writer.
func StallGuardRoot(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.Body != nil && r.ContentLength != 0 {
			if window := RequestBodyStallWindow(); window > 0 {
				r.Body = &stallGuardReader{
					rc:     http.NewResponseController(w),
					body:   r.Body,
					window: window,
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

type stallGuardReader struct {
	rc       *http.ResponseController
	body     io.ReadCloser
	window   time.Duration
	disabled bool
}

func (s *stallGuardReader) Read(p []byte) (int, error) {
	if s.disabled {
		return s.body.Read(p)
	}
	if err := s.rc.SetReadDeadline(time.Now().Add(s.window)); err != nil {
		// Deadlines unsupported on this transport (test recorders, custom
		// writers): reading unguarded beats rejecting every request.
		s.disabled = true
		return s.body.Read(p)
	}
	n, err := s.body.Read(p)
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return n, fmt.Errorf("%w: no body data for %s", ErrRequestBodyStalled, s.window)
	case errors.Is(err, io.EOF):
		// Full body consumed: clear the deadline so it cannot leak into
		// reading the next request on a reused keep-alive connection.
		_ = s.rc.SetReadDeadline(time.Time{})
		return n, err
	default:
		return n, err
	}
}

func (s *stallGuardReader) Close() error {
	return s.body.Close()
}

var _ io.ReadCloser = (*stallGuardReader)(nil)
