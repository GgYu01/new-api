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
