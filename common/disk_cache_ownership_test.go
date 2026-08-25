package common

import (
	"bytes"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCleanupOldDiskCacheFilesDoesNotDeleteActiveLongRequestSpool(t *testing.T) {
	configureBodyStorageDiskCache(t, 1, 64)
	payload := bytes.Repeat([]byte("active-long-request"), 128*1024)
	storage, err := CreateBodyStorageFromReader(bytes.NewReader(payload), int64(len(payload)), 8<<20)
	require.NoError(t, err)
	require.True(t, storage.IsDisk())

	disk, ok := storage.(*diskStorage)
	require.True(t, ok)
	require.True(t, isActiveDiskCacheFile(disk.filePath))
	old := time.Now().Add(-30 * time.Minute)
	require.NoError(t, os.Chtimes(disk.filePath, old, old))

	require.NoError(t, CleanupOldDiskCacheFiles(10*time.Minute))
	require.FileExists(t, disk.filePath)

	replay, err := storage.NewReader()
	require.NoError(t, err, "maintenance must not unlink a spool owned by an active request")
	require.NoError(t, replay.Close())
	require.NoError(t, storage.Close())
	require.False(t, isActiveDiskCacheFile(disk.filePath))
	require.NoFileExists(t, disk.filePath)
}

func TestCleanupOldDiskCacheFilesDeletesUnownedCrashOrphan(t *testing.T) {
	cacheDir := configureBodyStorageDiskCache(t, 1, 64)
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))
	orphanPath := cacheDir + string(os.PathSeparator) + "body-crash-orphan.tmp"
	payload := []byte("orphaned partial body")
	require.NoError(t, os.WriteFile(orphanPath, payload, 0o600))
	IncrementDiskFiles(int64(len(payload)))
	t.Cleanup(func() {
		if _, err := os.Stat(orphanPath); err == nil {
			_ = os.Remove(orphanPath)
			DecrementDiskFiles(int64(len(payload)))
		}
	})
	old := time.Now().Add(-30 * time.Minute)
	require.NoError(t, os.Chtimes(orphanPath, old, old))
	require.False(t, isActiveDiskCacheFile(orphanPath))

	statsBefore := GetDiskCacheStats()
	require.NoError(t, CleanupOldDiskCacheFiles(10*time.Minute))
	require.NoFileExists(t, orphanPath)
	statsAfter := GetDiskCacheStats()
	require.Equal(t, statsBefore.ActiveDiskFiles-1, statsAfter.ActiveDiskFiles)
	require.Equal(t, statsBefore.CurrentDiskUsageBytes-int64(len(payload)), statsAfter.CurrentDiskUsageBytes)
}
