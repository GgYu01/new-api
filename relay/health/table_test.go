package health

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestWaitOrProbeSingleflightLimitsProbes(t *testing.T) {
	for _, success := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovered", false: "still-dead"}[success], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				table := NewTable(time.Minute)
				key := Key{TransportPathID: "primary", Provider: "cpa", ModelDomain: "gpt", CredentialFP: "acct-1"}
				now := time.Now()
				table.MarkUnhealthy(key, now.Add(-time.Minute))
				var probes atomic.Int32
				started, release := make(chan struct{}), make(chan struct{})
				probe := func(ctx context.Context) error {
					if probes.Add(1) == 1 {
						close(started)
					}
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
					if !success {
						return errors.New("connection refused")
					}
					return nil
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				var wg sync.WaitGroup
				const waiters = 32
				for i := 0; i < waiters; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); _, _ = table.WaitOrProbe(ctx, key, now, probe) }()
				}
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("probe did not start")
				}
				synctest.Wait() // All followers have joined the exact in-flight probe.
				if got := probes.Load(); got != 1 {
					t.Fatalf("in-flight probes=%d want 1", got)
				}
				close(release)
				wg.Wait()
				if got := probes.Load(); got != 1 {
					t.Fatalf("probes=%d want 1", got)
				}
				snap := table.Snapshot(key, now)
				want := StateUnhealthy
				if success {
					want = StateHealthy
				}
				if snap.State != want || snap.ProbeRuns != 1 {
					t.Fatalf("snapshot=%+v want state=%s one probe", snap, want)
				}
			})
		})
	}
}

func TestG010HealthCooldownAndExpiry(t *testing.T) {
	table := NewTable(time.Minute)
	key := Key{TransportPathID: "dead"}
	now := time.Now()
	table.MarkUnhealthy(key, now)
	calls := 0
	probe := func(context.Context) error { calls++; return errors.New("connection refused") }
	for i := 0; i < 200; i++ {
		_, err := table.WaitOrProbe(context.Background(), key, now, probe)
		if !errors.Is(err, ErrCooldown) {
			t.Fatalf("cooldown error=%v", err)
		}
	}
	if calls != 0 {
		t.Fatalf("probed during cooldown: %d", calls)
	}
	expiry := now.Add(time.Minute)
	if table.Snapshot(key, expiry.Add(time.Second)).State != StateUnhealthy {
		t.Fatal("expiry must not imply recovery")
	}
	for i := 0; i < 200; i++ {
		_, _ = table.WaitOrProbe(context.Background(), key, expiry, probe)
	}
	if calls != 1 {
		t.Fatalf("probes at expiry=%d want 1", calls)
	}
}

func TestG010HealthWaiterCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		table := NewTable(time.Second)
		key := Key{TransportPathID: "dead"}
		now := time.Now()
		table.MarkUnhealthy(key, now.Add(-time.Second))
		started, release, leaderDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
		go func() {
			defer close(leaderDone)
			_, _ = table.WaitOrProbe(context.Background(), key, now, func(context.Context) error { close(started); <-release; return nil })
		}()
		<-started
		ctx, cancel := context.WithCancel(context.Background())
		waiterDone := make(chan error, 1)
		go func() {
			_, err := table.WaitOrProbe(ctx, key, now, func(context.Context) error { t.Error("waiter must not dispatch"); return nil })
			waiterDone <- err
		}()
		synctest.Wait()
		cancel()
		if err := <-waiterDone; !errors.Is(err, context.Canceled) {
			t.Fatalf("waiter error=%v", err)
		}
		close(release)
		<-leaderDone
	})
}
