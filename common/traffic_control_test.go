package common

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTrafficControllerHybridDefaultsAndLazyRefill(t *testing.T) {
	start := time.Unix(0, 0)
	cfg := DefaultTrafficControlConfig()
	cfg.Mode = TrafficControlModeHybrid
	cfg.Burst, cfg.GlobalRPM, cfg.MaxActiveRequests = 2, 60, 2
	controller, err := NewTrafficController(cfg, start)
	require.NoError(t, err)
	first, _, _ := controller.Admit(start)
	second, _, _ := controller.Admit(start)
	require.NotNil(t, first)
	require.NotNil(t, second)
	third, retry, reason := controller.Admit(start)
	require.Nil(t, third)
	require.Equal(t, TrafficControlRejectActive, reason)
	require.Equal(t, time.Second, retry)
	first.Release()
	second.Release()
	third, _, reason = controller.Admit(start)
	require.Nil(t, third)
	require.Equal(t, TrafficControlRejectRPM, reason)
	refilled, _, _ := controller.Admit(start.Add(1 * time.Second))
	require.NotNil(t, refilled)
	refilled.Release()
	metrics := controller.Metrics()
	require.Equal(t, int64(2), metrics.ActivePeak)
	require.Equal(t, int64(1), metrics.RejectedActiveTotal)
	require.Equal(t, int64(1), metrics.RejectedRPMTotal)
}

func TestTrafficControllerReleaseIsExactlyOnceAndConfigIsAtomic(t *testing.T) {
	controller, err := NewTrafficController(DefaultTrafficControlConfig(), time.Unix(0, 0))
	require.NoError(t, err)
	lease, _, _ := controller.Admit(time.Unix(0, 0))
	require.NotNil(t, lease)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); lease.Release() }()
	}
	wg.Wait()
	require.Zero(t, controller.Metrics().ActiveCurrent)
	cfg := DefaultTrafficControlConfig()
	cfg.Mode = TrafficControlModeOff
	require.NoError(t, controller.UpdateConfig(cfg, time.Unix(1, 0)))
	require.Equal(t, TrafficControlModeOff, controller.Config().Mode)
}

func TestTrafficControlOptionsRejectQueueAndParseDefaults(t *testing.T) {
	cfg, err := TrafficControlConfigFromOptions(map[string]string{TrafficControlGlobalRPMOption: "240"})
	require.NoError(t, err)
	require.Equal(t, int64(240), cfg.GlobalRPM)
	require.Equal(t, int64(32), cfg.Burst)
	_, err = TrafficControlConfigFromOptions(map[string]string{TrafficControlWaitingQueueOption: "1"})
	require.Error(t, err)
}

func TestTrafficControllerAccepts240ActiveAndRejects241stImmediately(t *testing.T) {
	cfg := DefaultTrafficControlConfig()
	cfg.GlobalRPM, cfg.Burst, cfg.MaxActiveRequests = 240, 240, 240
	controller, err := NewTrafficController(cfg, time.Unix(0, 0))
	require.NoError(t, err)
	leases := make([]*TrafficLease, 0, 240)
	for i := 0; i < 240; i++ {
		lease, _, reason := controller.Admit(time.Unix(0, 0))
		require.NotNil(t, lease)
		require.Empty(t, reason)
		leases = append(leases, lease)
	}
	rejected, retry, reason := controller.Admit(time.Unix(0, 0))
	require.Nil(t, rejected)
	require.Equal(t, time.Second, retry)
	require.Equal(t, TrafficControlRejectActive, reason)
	for _, lease := range leases {
		lease.Release()
	}
	require.Zero(t, controller.Metrics().ActiveCurrent)
}

func TestDefaultTrafficControlIsConcurrency240Queue0(t *testing.T) {
	cfg := DefaultTrafficControlConfig()
	require.True(t, cfg.Enabled)
	require.Equal(t, TrafficControlModeConcurrency, cfg.Mode)
	require.Equal(t, int64(240), cfg.MaxActiveRequests)
	require.Zero(t, cfg.WaitingQueue)
	require.Zero(t, cfg.WaitingTimeoutMs)
	require.NoError(t, ValidateTrafficControlConfig(cfg))
}

func TestSameValueUpdateIsNoOpAndDoesNotMintBurst(t *testing.T) {
	start := time.Unix(0, 0)
	cfg := DefaultTrafficControlConfig()
	cfg.Mode = TrafficControlModeHybrid
	cfg.GlobalRPM, cfg.Burst = 60, 2
	controller, err := NewTrafficController(cfg, start)
	require.NoError(t, err)
	first, _, _ := controller.Admit(start)
	require.NotNil(t, first)
	first.Release()
	before := controller.Metrics().Revision
	// Identical save at the same instant: strict no-op.
	require.NoError(t, controller.UpdateConfig(cfg, start))
	require.Equal(t, before, controller.Metrics().Revision)
	// The bucket must still hold exactly one token: a second immediate admit
	// succeeds but a third within the same instant is RPM-rejected. If the
	// save had reset the bucket, the third admit would wrongly succeed.
	second, _, reason := controller.Admit(start)
	require.NotNil(t, second)
	require.Empty(t, reason)
	second.Release()
	third, _, reason := controller.Admit(start)
	require.Nil(t, third)
	require.Equal(t, TrafficControlRejectRPM, reason)
	// The same config saved at a much later time must not mint tokens either;
	// any refill must come from real elapsed time in Admit, not from saves.
	require.NoError(t, controller.UpdateConfig(cfg, start.Add(1*time.Hour)))
	fourth, _, reason := controller.Admit(start)
	require.Nil(t, fourth)
	require.Equal(t, TrafficControlRejectRPM, reason)
}

func TestRPMChangePreservesTokensAtOldRate(t *testing.T) {
	start := time.Unix(0, 0)
	newController := func() *TrafficController {
		cfg := DefaultTrafficControlConfig()
		cfg.Mode = TrafficControlModeHybrid
		cfg.GlobalRPM, cfg.Burst, cfg.MaxActiveRequests = 60, 2, 100
		controller, err := NewTrafficController(cfg, start)
		require.NoError(t, err)
		first, _, _ := controller.Admit(start)
		second, _, _ := controller.Admit(start)
		require.NotNil(t, first)
		require.NotNil(t, second)
		first.Release()
		second.Release()
		return controller
	}
	changed := DefaultTrafficControlConfig()
	changed.Mode = TrafficControlModeHybrid
	changed.GlobalRPM, changed.Burst, changed.MaxActiveRequests = 120, 10, 100
	// A same-instant change must not reset the empty bucket.
	controller := newController()
	require.NoError(t, controller.UpdateConfig(changed, start))
	third, _, reason := controller.Admit(start)
	require.Nil(t, third)
	require.Equal(t, TrafficControlRejectRPM, reason)
	// A change 30 seconds later refills at the OLD rate (60/min × 0.5min = 30
	// pending tokens) and then clamps to the new burst of 10.
	controller = newController()
	require.NoError(t, controller.UpdateConfig(changed, start.Add(30*time.Second)))
	for i := 0; i < 10; i++ {
		lease, _, reason := controller.Admit(start.Add(30 * time.Second))
		require.NotNil(t, lease)
		require.Empty(t, reason)
		defer lease.Release()
	}
	_, _, reason = controller.Admit(start.Add(30 * time.Second))
	require.Equal(t, TrafficControlRejectRPM, reason)
}

func TestEnteringRPMModeSeedsFreshBucket(t *testing.T) {
	start := time.Unix(0, 0)
	cfg := DefaultTrafficControlConfig()
	cfg.GlobalRPM, cfg.Burst = 60, 3
	controller, err := NewTrafficController(cfg, start)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		lease, _, _ := controller.Admit(start)
		require.NotNil(t, lease)
		defer lease.Release()
	}
	// Concurrency mode never touched the token bucket.
	rpmCfg := cfg
	rpmCfg.Mode = TrafficControlModeRPM
	require.NoError(t, controller.UpdateConfig(rpmCfg, start))
	require.NotEmpty(t, controller.Metrics().Revision)
	lease, _, reason := controller.Admit(start)
	require.NotNil(t, lease)
	require.Empty(t, reason)
	lease.Release()
}

func TestOffAndRPMModesStillCountActive(t *testing.T) {
	start := time.Unix(0, 0)
	cfg := DefaultTrafficControlConfig()
	cfg.Mode = TrafficControlModeOff
	cfg.MaxActiveRequests = 2
	controller, err := NewTrafficController(cfg, start)
	require.NoError(t, err)
	var leases []*TrafficLease
	for i := 0; i < 3; i++ {
		lease, _, reason := controller.Admit(start)
		require.NotNil(t, lease)
		require.Empty(t, reason)
		leases = append(leases, lease)
	}
	require.Equal(t, int64(3), controller.Metrics().ActiveCurrent)
	// Hot-switch to concurrency: the off-mode leases are still counted, so the
	// ceiling applies immediately to new requests only.
	conc := cfg
	conc.Mode = TrafficControlModeConcurrency
	require.NoError(t, controller.UpdateConfig(conc, start))
	rejected, _, reason := controller.Admit(start)
	require.Nil(t, rejected)
	require.Equal(t, TrafficControlRejectActive, reason)
	for _, lease := range leases {
		lease.Release()
	}
	require.Zero(t, controller.Metrics().ActiveCurrent)
	leased, _, _ := controller.Admit(start)
	require.NotNil(t, leased)
	leased.Release()
}

func TestHybridDoesNotConsumeRPMTokenWhenConcurrencyFull(t *testing.T) {
	start := time.Unix(0, 0)
	cfg := DefaultTrafficControlConfig()
	cfg.Mode = TrafficControlModeHybrid
	cfg.GlobalRPM, cfg.Burst, cfg.MaxActiveRequests = 60, 2, 1
	controller, err := NewTrafficController(cfg, start)
	require.NoError(t, err)
	first, _, _ := controller.Admit(start)
	require.NotNil(t, first)
	rejected, _, reason := controller.Admit(start)
	require.Nil(t, rejected)
	require.Equal(t, TrafficControlRejectActive, reason)
	// The active-rejected request must not have consumed an RPM token: the
	// bucket held 2 tokens, admit #1 spent one, and after releasing it the
	// next admit must still find exactly the second one.
	_, _, reason = controller.Admit(start)
	require.Equal(t, TrafficControlRejectActive, reason)
	first.Release()
	second, _, _ := controller.Admit(start)
	require.NotNil(t, second)
	second.Release()
	_, _, reason = controller.Admit(start)
	require.Equal(t, TrafficControlRejectRPM, reason)
}
func TestRevisionAdvancesOnlyOnChange(t *testing.T) {
	start := time.Unix(0, 0)
	controller, err := NewTrafficController(DefaultTrafficControlConfig(), start)
	require.NoError(t, err)
	base := controller.Metrics().Revision
	changed := DefaultTrafficControlConfig()
	changed.MaxActiveRequests = 180
	require.NoError(t, controller.UpdateConfig(changed, start))
	require.Equal(t, base+1, controller.Metrics().Revision)
	require.NoError(t, controller.UpdateConfig(changed, start))
	require.Equal(t, base+1, controller.Metrics().Revision)
}

func TestSnapshotTrafficControlOptionsFallsBackToDefaults(t *testing.T) {
	options := SnapshotTrafficControlOptions(map[string]string{})
	cfg, err := TrafficControlConfigFromOptions(options)
	require.NoError(t, err)
	require.Equal(t, DefaultTrafficControlConfig(), cfg)
	partial := SnapshotTrafficControlOptions(map[string]string{TrafficControlMaxActiveOption: "180"})
	cfg, err = TrafficControlConfigFromOptions(partial)
	require.NoError(t, err)
	require.Equal(t, int64(180), cfg.MaxActiveRequests)
	require.Equal(t, TrafficControlModeConcurrency, cfg.Mode)
}

func TestTrafficControllerAccepts1000ActiveAndRejects1001stImmediately(t *testing.T) {
	start := time.Unix(0, 0)
	cfg := DefaultTrafficControlConfig()
	cfg.MaxActiveRequests = 1000
	controller, err := NewTrafficController(cfg, start)
	require.NoError(t, err)

	leases := make([]*TrafficLease, 1000)
	for i := 0; i < 1000; i++ {
		lease, _, reason := controller.Admit(start)
		require.NotNil(t, lease, "request %d should be admitted", i+1)
		require.Empty(t, reason)
		leases[i] = lease
	}
	require.Equal(t, int64(1000), controller.Metrics().ActiveCurrent)

	// 1001st must be rejected immediately with Active rejection
	rejected, _, reason := controller.Admit(start)
	require.Nil(t, rejected)
	require.Equal(t, TrafficControlRejectActive, reason)

	// Releasing all 1000 leases
	for _, lease := range leases {
		lease.Release()
	}
	require.Equal(t, int64(0), controller.Metrics().ActiveCurrent)

	// Now a new request is admitted again
	admitted, _, _ := controller.Admit(start)
	require.NotNil(t, admitted)
	admitted.Release()
}
