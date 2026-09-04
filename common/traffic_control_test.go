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
