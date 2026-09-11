package lifecycle

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/stretchr/testify/require"
)

func Test_CanRecover_budgetExhausted(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.PrecommitRecoveryBudget = 50 * time.Millisecond
		restore := SetTestConfig(cfg)
		defer restore()

		lr := New(context.Background(), "root-budget", true, types.RelayFormatOpenAI)
		defer lr.Cleanup()
		apiErr := types.NewErrorWithStatusCode(
			fmt.Errorf("upstream overloaded"),
			types.ErrorCodeBadResponseStatusCode,
			http.StatusServiceUnavailable,
		)
		ok, _ := CanRecover(lr, apiErr, 7)
		require.True(t, ok, "budget starts unexhausted")

		time.Sleep(60 * time.Millisecond)
		synctest.Wait()
		ok, class := CanRecover(lr, apiErr, 7)
		require.False(t, ok, "exhausted precommit budget must block recovery")
		require.Equal(t, ClassBudgetExhausted, class)
	})
}

func TestG010DefaultRecoveryBudgetAndAttemptCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		restore := SetTestConfig(DefaultConfig())
		defer restore()
		lr := New(context.Background(), "g010-budget", true, types.RelayFormatOpenAI)
		defer lr.Cleanup()
		require.Equal(t, 30*time.Minute, lr.RemainingPrecommit())
		apiErr := ErrorFromStatus(&http.Response{StatusCode: 503}, nil)
		for i := 0; i < 8; i++ {
			ok, _ := CanRecover(lr, apiErr, 7)
			require.True(t, ok)
			_, _, err := lr.BeginAttempt()
			require.NoError(t, err)
			_, _, err = lr.BeginAttempt()
			require.Error(t, err, "no overlapping attempts")
			lr.EndAttempt(AttemptRecord{StatusCode: 503})
		}
		ok, class := CanRecover(lr, apiErr, 7)
		require.False(t, ok)
		require.Equal(t, ClassMaxAttempts, class)
		require.Equal(t, 8, lr.DispatchedAttempts())
		_, _, err := lr.BeginAttempt()
		require.Error(t, err)

		fresh := New(context.Background(), "g010-expiry", true, types.RelayFormatOpenAI)
		defer fresh.Cleanup()
		<-time.NewTimer(30 * time.Minute).C // Virtual expiry, not elapsed wall time.
		ok, class = CanRecover(fresh, apiErr, 7)
		require.False(t, ok)
		require.Equal(t, ClassBudgetExhausted, class)
		_, _, err = fresh.BeginAttempt()
		require.Error(t, err)
	})
}

func Test_CanRecover_maxAttemptsCap(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := DefaultConfig()
		cfg.MaxUpstreamAttempts = 2
		restore := SetTestConfig(cfg)
		defer restore()

		lr := New(context.Background(), "root-attempts", true, types.RelayFormatOpenAI)
		defer lr.Cleanup()
		apiErr := types.NewErrorWithStatusCode(
			fmt.Errorf("upstream overloaded"),
			types.ErrorCodeBadResponseStatusCode,
			http.StatusServiceUnavailable,
		)
		for i := 0; i < 2; i++ {
			_, _, err := lr.BeginAttempt()
			require.NoError(t, err)
			lr.EndAttempt(AttemptRecord{StatusCode: 503})
		}
		ok, class := CanRecover(lr, apiErr, 7)
		require.False(t, ok, "attempt cap must block further recovery")
		require.Equal(t, ClassMaxAttempts, class)
	})
}
