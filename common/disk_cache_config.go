package common

import (
	"errors"
	"sync"
	"sync/atomic"
)

var ErrDiskCacheCapacityExhausted = errors.New("disk cache capacity exhausted")

// DiskCacheConfig is updated by the performance settings package.
type DiskCacheConfig struct {
	// Enabled controls disk-backed request storage.
	Enabled bool
	// ThresholdMB is the request size that starts disk storage.
	ThresholdMB int
	// MaxSizeMB is the aggregate disk-storage budget.
	MaxSizeMB int
	// Path is the disk-storage directory.
	Path string
}

// Global disk-cache configuration.
var diskCacheConfig = DiskCacheConfig{
	Enabled:     false,
	ThresholdMB: 10,
	MaxSizeMB:   1024,
	Path:        "",
}
var diskCacheConfigMu sync.RWMutex

// GetDiskCacheConfig returns the current disk-cache configuration.
func GetDiskCacheConfig() DiskCacheConfig {
	diskCacheConfigMu.RLock()
	defer diskCacheConfigMu.RUnlock()
	return diskCacheConfig
}

// SetDiskCacheConfig replaces the disk-cache configuration.
func SetDiskCacheConfig(config DiskCacheConfig) {
	diskCacheConfigMu.Lock()
	defer diskCacheConfigMu.Unlock()
	diskCacheConfig = config
}

// IsDiskCacheEnabled reports whether disk-backed request storage is enabled.
func IsDiskCacheEnabled() bool {
	diskCacheConfigMu.RLock()
	defer diskCacheConfigMu.RUnlock()
	return diskCacheConfig.Enabled
}

// GetDiskCacheThresholdBytes returns the spill threshold in bytes.
func GetDiskCacheThresholdBytes() int64 {
	diskCacheConfigMu.RLock()
	defer diskCacheConfigMu.RUnlock()
	return int64(diskCacheConfig.ThresholdMB) << 20
}

// GetDiskCacheMaxSizeBytes returns the disk budget in bytes.
func GetDiskCacheMaxSizeBytes() int64 {
	diskCacheConfigMu.RLock()
	defer diskCacheConfigMu.RUnlock()
	return int64(diskCacheConfig.MaxSizeMB) << 20
}

// GetDiskCachePath returns the configured cache directory.
func GetDiskCachePath() string {
	diskCacheConfigMu.RLock()
	defer diskCacheConfigMu.RUnlock()
	return diskCacheConfig.Path
}

// DiskCacheStats is a read-only request-storage snapshot.
type DiskCacheStats struct {
	// ActiveDiskFiles counts live owned disk files.
	ActiveDiskFiles int64 `json:"active_disk_files"`
	// CurrentDiskUsageBytes counts reserved disk bytes.
	CurrentDiskUsageBytes int64 `json:"current_disk_usage_bytes"`
	// ActiveMemoryBuffers counts live in-memory bodies.
	ActiveMemoryBuffers int64 `json:"active_memory_buffers"`
	// CurrentMemoryUsageBytes counts live in-memory body bytes.
	CurrentMemoryUsageBytes int64 `json:"current_memory_usage_bytes"`
	// DiskCacheHits counts disk-storage decisions.
	DiskCacheHits int64 `json:"disk_cache_hits"`
	// MemoryCacheHits counts memory-storage decisions.
	MemoryCacheHits int64 `json:"memory_cache_hits"`
	// MaxDiskCacheBytes is the configured disk budget.
	DiskCacheMaxBytes int64 `json:"disk_cache_max_bytes"`
	// ThresholdBytes is the spill threshold.
	DiskCacheThresholdBytes int64 `json:"disk_cache_threshold_bytes"`
}

var diskCacheStats DiskCacheStats

// GetDiskCacheStats returns a request-storage snapshot.
func GetDiskCacheStats() DiskCacheStats {
	stats := DiskCacheStats{
		ActiveDiskFiles:         atomic.LoadInt64(&diskCacheStats.ActiveDiskFiles),
		CurrentDiskUsageBytes:   atomic.LoadInt64(&diskCacheStats.CurrentDiskUsageBytes),
		ActiveMemoryBuffers:     atomic.LoadInt64(&diskCacheStats.ActiveMemoryBuffers),
		CurrentMemoryUsageBytes: atomic.LoadInt64(&diskCacheStats.CurrentMemoryUsageBytes),
		DiskCacheHits:           atomic.LoadInt64(&diskCacheStats.DiskCacheHits),
		MemoryCacheHits:         atomic.LoadInt64(&diskCacheStats.MemoryCacheHits),
		DiskCacheMaxBytes:       GetDiskCacheMaxSizeBytes(),
		DiskCacheThresholdBytes: GetDiskCacheThresholdBytes(),
	}
	return stats
}

// IncrementDiskFiles records a newly owned disk file.
func IncrementDiskFiles(size int64) {
	incrementDiskFileCount()
	atomic.AddInt64(&diskCacheStats.CurrentDiskUsageBytes, size)
}

// DecrementDiskFiles releases an owned disk file.
func DecrementDiskFiles(size int64) {
	decrementDiskFileCount()
	releaseDiskCacheBytes(size)
}

func incrementDiskFileCount() {
	atomic.AddInt64(&diskCacheStats.ActiveDiskFiles, 1)
}

func decrementDiskFileCount() {
	decrementInt64NonNegative(&diskCacheStats.ActiveDiskFiles, 1)
}

// reserveDiskCacheBytes atomically admits bytes into the shared disk budget.
// The usage counter includes both completed files and bytes reserved by a
// body that is currently being streamed, closing the check-then-write race
// between concurrent large requests.
func reserveDiskCacheBytes(size int64) error {
	if size <= 0 {
		return nil
	}
	config := GetDiskCacheConfig()
	if !config.Enabled {
		return ErrDiskCacheCapacityExhausted
	}
	maxBytes := int64(config.MaxSizeMB) << 20
	if maxBytes <= 0 || size > maxBytes {
		return ErrDiskCacheCapacityExhausted
	}
	for {
		current := atomic.LoadInt64(&diskCacheStats.CurrentDiskUsageBytes)
		if current < 0 {
			current = 0
		}
		if current > maxBytes-size {
			return ErrDiskCacheCapacityExhausted
		}
		if atomic.CompareAndSwapInt64(&diskCacheStats.CurrentDiskUsageBytes, current, current+size) {
			return nil
		}
	}
}

func releaseDiskCacheBytes(size int64) {
	if size <= 0 {
		return
	}
	decrementInt64NonNegative(&diskCacheStats.CurrentDiskUsageBytes, size)
}

func decrementInt64NonNegative(target *int64, delta int64) {
	for {
		current := atomic.LoadInt64(target)
		next := current - delta
		if next < 0 {
			next = 0
		}
		if atomic.CompareAndSwapInt64(target, current, next) {
			return
		}
	}
}

// IncrementMemoryBuffers records a newly owned memory body.
func IncrementMemoryBuffers(size int64) {
	atomic.AddInt64(&diskCacheStats.ActiveMemoryBuffers, 1)
	atomic.AddInt64(&diskCacheStats.CurrentMemoryUsageBytes, size)
}

// DecrementMemoryBuffers releases an owned memory body.
func DecrementMemoryBuffers(size int64) {
	atomic.AddInt64(&diskCacheStats.ActiveMemoryBuffers, -1)
	atomic.AddInt64(&diskCacheStats.CurrentMemoryUsageBytes, -size)
}

// IncrementDiskCacheHits records a disk-storage decision.
func IncrementDiskCacheHits() {
	atomic.AddInt64(&diskCacheStats.DiskCacheHits, 1)
}

// IncrementMemoryCacheHits records a memory-storage decision.
func IncrementMemoryCacheHits() {
	atomic.AddInt64(&diskCacheStats.MemoryCacheHits, 1)
}

// ResetDiskCacheStats resets decision counters without changing active usage.
func ResetDiskCacheStats() {
	atomic.StoreInt64(&diskCacheStats.DiskCacheHits, 0)
	atomic.StoreInt64(&diskCacheStats.MemoryCacheHits, 0)
}

// ResetDiskCacheUsage clears active-usage counters after external cleanup.
func ResetDiskCacheUsage() {
	atomic.StoreInt64(&diskCacheStats.ActiveDiskFiles, 0)
	atomic.StoreInt64(&diskCacheStats.CurrentDiskUsageBytes, 0)
}

// SyncDiskCacheStats reconciles counters with unowned files on disk.
func SyncDiskCacheStats() {
	fileCount, totalSize, err := GetDiskCacheInfo()
	if err != nil {
		return
	}
	atomic.StoreInt64(&diskCacheStats.ActiveDiskFiles, int64(fileCount))
	atomic.StoreInt64(&diskCacheStats.CurrentDiskUsageBytes, totalSize)
}

// IsDiskCacheAvailable reports whether the requested bytes fit the disk budget.
func IsDiskCacheAvailable(requestSize int64) bool {
	if !IsDiskCacheEnabled() {
		return false
	}
	maxBytes := GetDiskCacheMaxSizeBytes()
	currentUsage := atomic.LoadInt64(&diskCacheStats.CurrentDiskUsageBytes)
	return requestSize >= 0 && requestSize <= maxBytes && currentUsage <= maxBytes-requestSize
}
