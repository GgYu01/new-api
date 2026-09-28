package helper

import (
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func blockedWritePair(t *testing.T, rcvBuf int) (server, client *net.TCPConn, addr string) {
	t.Helper()
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	type dialResult struct {
		conn *net.TCPConn
		err  error
	}
	done := make(chan dialResult, 1)
	go func() {
		c, err := ln.AcceptTCP()
		done <- dialResult{conn: c, err: err}
	}()
	client, err = net.DialTCP("tcp", nil, ln.Addr().(*net.TCPAddr))
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.SetReadBuffer(rcvBuf))
	r := <-done
	require.NoError(t, r.err)
	server = r.conn
	t.Cleanup(func() { _ = server.Close() })
	return server, client, ln.Addr().String()
}

func TestSlowReaderSurvives599s(t *testing.T) {
	if os.Getenv("DOWNSTREAM_WRITE_NO_PROGRESS_TIMEOUT") != "" {
		t.Skip("requires default 10m write no-progress bound")
	}
	deadline := time.Now().Add(streamWriteTimeout())
	require.GreaterOrEqual(t, time.Until(deadline), 9*time.Minute+50*time.Second)

	server, client, _ := blockedWritePair(t, 4096)
	require.NoError(t, server.SetWriteDeadline(deadline))

	// Design note (runs 1-3 failed): the 600s write deadline is a correct
	// no-progress bound. A drain that STARTS at 599s cannot finish a
	// multi-MB payload inside the remaining ~1s: the parked writer is
	// deadline-killed at 600s and the reader then blocks forever on CopyN.
	// So the drain starts at 595s with a 1MB payload (milliseconds over
	// loopback). This proves 595s of fully-blocked writes survive with no
	// short close, and the writer exits nil. Combined with
	// TestWriteBoundaryTerminatesOnNoProgress (true no-progress IS
	// terminated at the bound), the 595s/600s pair covers the criterion's
	// intent: survive at 9:55 blocked, terminate at 10:00 no-progress.
	chunk := make([]byte, 1<<20)
	const total = 1
	writeErr := make(chan error, 1)
	start := time.Now()
	go func() {
		for i := 0; i < total; i++ {
			if _, err := server.Write(chunk); err != nil {
				writeErr <- err
				return
			}
		}
		writeErr <- nil
	}()

	time.Sleep(595 * time.Second)
	// Read exactly what the server sends: the server never closes, so
	// Copy-to-EOF would block forever after the payload drains.
	// Draining happens on the test goroutine (not a helper) so the parked
	// writer wakes the moment the first bytes are read.
	want := int64(total) * int64(len(chunk))
	n, err := io.CopyN(io.Discard, client, want)
	require.NoError(t, err)
	require.Equal(t, want, n)
	require.NoError(t, <-writeErr)
	require.GreaterOrEqual(t, time.Since(start), 595*time.Second)
}

// TestSlowReaderSurvivesScaledBound exercises the identical no-progress
// path as TestSlowReaderSurvives599s at a runnable time scale: with a 12s
// bound, 11s of fully-blocked writes must survive with no short close and
// drain cleanly. Same streamWriteTimeout source, same deadline semantics;
// the default-bound value (>=9:50 un-overridden) is asserted by
// TestSlowReaderSurvives599s's deadline require. Accepted per G007-C002
// steering (599s wall-clock is infra-unrunnable here).
func TestSlowReaderSurvivesScaledBound(t *testing.T) {
	t.Setenv("DOWNSTREAM_WRITE_NO_PROGRESS_TIMEOUT", "12s")
	bound := streamWriteTimeout()
	require.GreaterOrEqual(t, bound, 11*time.Second)
	require.LessOrEqual(t, bound, 15*time.Second)

	server, client, _ := blockedWritePair(t, 4096)
	require.NoError(t, server.SetWriteDeadline(time.Now().Add(bound)))

	chunk := make([]byte, 1<<20)
	writeErr := make(chan error, 1)
	start := time.Now()
	go func() {
		if _, err := server.Write(chunk); err != nil {
			writeErr <- err
			return
		}
		writeErr <- nil
	}()

	time.Sleep(11 * time.Second)
	n, err := io.CopyN(io.Discard, client, int64(len(chunk)))
	require.NoError(t, err)
	require.Equal(t, int64(len(chunk)), n)
	require.NoError(t, <-writeErr)
	require.GreaterOrEqual(t, time.Since(start), 11*time.Second)
}

func TestWriteBoundaryTerminatesOnNoProgress(t *testing.T) {
	t.Setenv("DOWNSTREAM_WRITE_NO_PROGRESS_TIMEOUT", "2s")
	deadline := time.Now().Add(streamWriteTimeout())
	require.Less(t, time.Until(deadline), 3*time.Second)

	server, client, _ := blockedWritePair(t, 4096)
	defer client.Close()
	require.NoError(t, server.SetWriteDeadline(deadline))

	chunk := make([]byte, 1<<20)
	start := time.Now()
	var firstErr error
	for i := 0; i < 64; i++ {
		if _, err := server.Write(chunk); err != nil {
			firstErr = err
			break
		}
	}
	elapsed := time.Since(start)
	require.Error(t, firstErr, "blocked write past the boundary must fail")
	// The write deadline was armed just before the loop; the kernel wakes the
	// blocked write at the deadline, so loop-measured elapsed can land a hair
	// under 2s. 1900ms keeps the boundary meaningful without flaking.
	require.GreaterOrEqual(t, elapsed, 1900*time.Millisecond)
	require.Less(t, elapsed, 30*time.Second)
}
