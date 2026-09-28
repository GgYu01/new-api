package relay

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/relay/lifecycle"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

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

// Test_LogicalRequest_phaseTimersReleaseGoroutines starts N logical requests
// with precommit-budget + max-lifetime timers, then cancels/exhausts them and
// asserts timer/goroutine counts return to baseline. Uses channels + bounded
// timeouts; no sleep longer than 5s.
func Test_LogicalRequest_phaseTimersReleaseGoroutines(t *testing.T) {
	cfg := lifecycle.DefaultConfig()
	cfg.PrecommitRecoveryBudget = 30 * time.Minute
	cfg.LogicalRequestMaxLifetime = 60 * time.Minute
	cfg.AttemptFirstEventTimeout = 10 * time.Minute
	restore := lifecycle.SetTestConfig(cfg)
	defer restore()

	runtime.GC()
	baseline := runtime.NumGoroutine()

	const n = 24
	type result struct {
		err error
	}
	done := make(chan result, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			lr := lifecycle.New(context.Background(), "leak-root", true, types.RelayFormatOpenAI)
			_, attemptCtx, err := lr.BeginAttempt()
			if err != nil {
				lr.Cleanup()
				done <- result{err: err}
				return
			}
			select {
			case <-attemptCtx.Done():
				lr.Cleanup()
				done <- result{err: attemptCtx.Err()}
				return
			default:
			}
			if i%2 == 0 {
				lr.CancelRoot(lifecycle.CancelCausePrecommit)
			} else {
				lr.CancelRoot(lifecycle.CancelCauseRootDeadline)
			}
			lr.EndAttempt(lifecycle.AttemptRecord{CancelCause: lifecycle.CancelCauseAttemptEnd})
			lr.Cleanup()
			select {
			case <-lr.RootContext().Done():
			case <-time.After(2 * time.Second):
				done <- result{err: context.DeadlineExceeded}
				return
			}
			done <- result{}
		}(i)
	}

	joinDeadline := time.NewTimer(3 * time.Second)
	defer joinDeadline.Stop()
	for i := 0; i < n; i++ {
		select {
		case r := <-done:
			require.NoError(t, r.err)
		case <-joinDeadline.C:
			t.Fatalf("only %d/%d logical requests released", i, n)
		}
	}

	waitNumGoroutine(t, baseline, 8)
}

func Test_LogicalRequest_exhaustedBudgetReleasesAttemptTimer(t *testing.T) {
	cfg := lifecycle.DefaultConfig()
	cfg.PrecommitRecoveryBudget = 30 * time.Millisecond
	cfg.LogicalRequestMaxLifetime = 80 * time.Millisecond
	restore := lifecycle.SetTestConfig(cfg)
	defer restore()

	runtime.GC()
	baseline := runtime.NumGoroutine()

	lr := lifecycle.New(context.Background(), "budget-root", false, types.RelayFormatOpenAI)
	_, attemptCtx, err := lr.BeginAttempt()
	require.NoError(t, err)

	select {
	case <-attemptCtx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("attempt context did not fire within bound")
	}
	lr.EndAttempt(lifecycle.AttemptRecord{CancelCause: lifecycle.CancelCausePrecommit})
	lr.Cleanup()
	select {
	case <-lr.RootContext().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("root lifetime timer did not release")
	}

	waitNumGoroutine(t, baseline, 8)
}
