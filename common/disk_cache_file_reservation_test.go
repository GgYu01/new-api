package common

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFileCacheReservationRejectsConcurrentOvercommitAndReleases(t *testing.T) {
	dir := t.TempDir()
	previous := GetDiskCacheConfig()
	SetDiskCacheConfig(DiskCacheConfig{Enabled: true, ThresholdMB: 1, MaxSizeMB: 1, Path: dir})
	ResetDiskCacheUsage()
	t.Cleanup(func() {
		SetDiskCacheConfig(previous)
		ResetDiskCacheUsage()
	})

	payload := string(make([]byte, 700<<10))
	type result struct {
		path string
		err  error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(1)
	for range 2 {
		go func() {
			start.Wait()
			path, err := WriteDiskCacheFileString(DiskCacheTypeFile, payload)
			results <- result{path: path, err: err}
		}()
	}
	start.Done()

	var success result
	var rejected int
	for range 2 {
		candidate := <-results
		if candidate.err == nil {
			success = candidate
		} else {
			require.True(t, errors.Is(candidate.err, ErrDiskCacheCapacityExhausted))
			rejected++
		}
	}
	require.NotEmpty(t, success.path)
	require.Equal(t, 1, rejected)
	require.Equal(t, int64(len(payload)), GetDiskCacheStats().CurrentDiskUsageBytes)
	require.NoError(t, os.Remove(success.path))
	ReleaseDiskCacheFileOwnership(success.path, int64(len(payload)))
	require.Zero(t, GetDiskCacheStats().CurrentDiskUsageBytes)
	require.Zero(t, GetDiskCacheStats().ActiveDiskFiles)
}
