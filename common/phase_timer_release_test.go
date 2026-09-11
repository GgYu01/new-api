package common

import (
	"context"
	"runtime"
	"testing"
	"time"
)

// waitNumGoroutine settles to a bound after GC. Polling is event-driven via
// a 10ms ticker with a hard 2s deadline — no sleep longer than 5s.
func waitNumGoroutine(t *testing.T, baseline, slack int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	runtime.GC()
	for {
		n := runtime.NumGoroutine()
		if n <= baseline+slack {
			return
		}
		select {
		case <-deadline.C:
			t.Fatalf("goroutine count %d did not return to baseline %d (slack %d)", n, baseline, slack)
		case <-tick.C:
			runtime.GC()
		}
	}
}

// Test_PhaseTimers_precommitAndLifetimeReleaseGoroutines starts N logical
// requests whose precommit budget and max-lifetime timers would otherwise
// pin a context timer goroutine for 30m/60m. Cancel/exhaust must return
// NumGoroutine to baseline. Uses channels + bounded timeouts, no sleep >5s.
func Test_PhaseTimers_precommitAndLifetimeReleaseGoroutines(t *testing.T) {
	runtime.GC()
	baseline := runtime.NumGoroutine()

	const n = 32
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			// Mimic LogicalRequest.New: a 60m lifetime timer plus a 30m
			// precommit timer. Both must be stopped/cancelled on cleanup so
			// the runtime timer goroutine (or context WithTimeout watcher)
			// does not leak for the remaining wall-clock lifetime.
			parent, lifeCancel := context.WithTimeout(context.Background(), DefaultLogicalRequestMaxLifetime)
			precommit, preCancel := context.WithTimeout(parent, DefaultPrecommitRecoveryBudget)
			root, rootCancel := context.WithCancel(precommit)
			select {
			case <-root.Done():
				t.Errorf("fresh root context already done: %v", root.Err())
			default:
			}
			rootCancel()
			preCancel()
			lifeCancel()
			<-root.Done()
			<-precommit.Done()
			<-parent.Done()
		}()
	}

	joined := 0
	joinDeadline := time.NewTimer(2 * time.Second)
	defer joinDeadline.Stop()
	for joined < n {
		select {
		case <-done:
			joined++
		case <-joinDeadline.C:
			t.Fatalf("only %d/%d workers joined", joined, n)
		}
	}

	waitNumGoroutine(t, baseline, 8)
}

func Test_PhaseTimers_cancelBeforeFireDoesNotBlock(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		close(started)
		ctx, cancel := context.WithTimeout(context.Background(), DefaultLogicalRequestMaxLifetime)
		pre, preCancel := context.WithTimeout(ctx, DefaultPrecommitRecoveryBudget)
		cancel()
		preCancel()
		<-ctx.Done()
		<-pre.Done()
		close(finished)
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("worker did not start")
	}
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("60m/30m timers did not release on cancel; would leak until fire")
	}
}
