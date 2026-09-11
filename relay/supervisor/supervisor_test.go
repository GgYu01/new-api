package supervisor

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relay/health"
	"github.com/QuantumNous/new-api/relay/transportpath"
)

func TestSupervisorSerialNoHedgeAndIndependentFailover(t *testing.T) {
	runner := New(health.NewTable(time.Minute))
	var concurrent atomic.Int32
	var maxConcurrent atomic.Int32
	var calls atomic.Int32
	dispatch := func(ctx context.Context, attempt Attempt) error {
		n := concurrent.Add(1)
		for {
			prev := maxConcurrent.Load()
			if n <= prev || maxConcurrent.CompareAndSwap(prev, n) {
				break
			}
		}
		defer concurrent.Add(-1)
		c := calls.Add(1)
		if c == 1 {
			return errors.New("connection refused")
		}
		return nil
	}
	res := runner.Run(context.Background(), []Attempt{
		{Path: transportpath.LegacyLocal8317, Channel: 1},
		{Path: transportpath.FallbackSSH, Channel: 1},
	}, dispatch)
	if res.Hedged || maxConcurrent.Load() != 1 {
		t.Fatalf("hedged=%v maxConcurrent=%d", res.Hedged, maxConcurrent.Load())
	}
	if res.Attempts != 2 || res.SucceededPath != transportpath.FallbackSSH {
		t.Fatalf("attempts=%d path=%s", res.Attempts, res.SucceededPath)
	}
}

func TestSupervisorSharedPathIsNotFailover(t *testing.T) {
	ch1 := transportpath.FromBaseURL("http://127.0.0.1:8317")
	ch9 := transportpath.FromBaseURL("http://127.0.0.1:8317")
	if transportpath.SerialFailover(ch1, ch9) {
		t.Fatal("channel 1 and 9 share 8317 and must not count as failover")
	}
	runner := New(health.NewTable(time.Minute))
	res := runner.Run(context.Background(), []Attempt{
		{Path: ch1, Channel: 1},
		{Path: ch9, Channel: 9},
	}, func(context.Context, Attempt) error {
		return errors.New("connection refused")
	})
	if !res.SamePathSwap {
		t.Fatal("switching channel on the same transport path must be flagged")
	}
	if res.SucceededPath != "" {
		t.Fatal("shared-path retries must not be reported as failover success")
	}
}
