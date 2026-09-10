package health

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWaitOrProbeSingleflightLimitsProbes(t *testing.T) {
	table := NewTable(time.Minute)
	key := Key{TransportPathID: "primary", Provider: "cpa", ModelDomain: "gpt", CredentialFP: "acct-1"}
	table.MarkUnhealthy(key, time.Now())

	var probes atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	probe := func(ctx context.Context) error {
		probes.Add(1)
		close(started)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ctx := context.Background()
	var wg sync.WaitGroup
	const waiters = 1000
	wg.Add(waiters)
	for i := 0; i < waiters; i++ {
		go func() {
			defer wg.Done()
			_, _ = table.WaitOrProbe(ctx, key, time.Now(), probe)
		}()
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not start")
	}
	close(release)
	wg.Wait()

	if got := probes.Load(); got != 1 {
		t.Fatalf("probes=%d want 1", got)
	}
	if snap := table.Snapshot(key, time.Now()); snap.State != StateHealthy {
		t.Fatalf("state=%s", snap.State)
	}
	if snap := table.Snapshot(key, time.Now()); snap.ProbeRuns != 1 {
		t.Fatalf("probe runs=%d want 1", snap.ProbeRuns)
	}
}
