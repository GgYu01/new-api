package common

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// DiskCacheType identifies a managed disk-cache file category.
type DiskCacheType string

const (
	DiskCacheTypeBody DiskCacheType = "body" // Request-body storage.
	DiskCacheTypeFile DiskCacheType = "file" // Uploaded-file storage.
)

// Shared cache directory name.
const diskCacheDir = "new-api-body-cache"

var activeDiskCacheFiles sync.Map

func registerActiveDiskCacheFile(filePath string) {
	activeDiskCacheFiles.Store(filepath.Clean(filePath), struct{}{})
}

func unregisterActiveDiskCacheFile(filePath string) {
	activeDiskCacheFiles.Delete(filepath.Clean(filePath))
}

func isActiveDiskCacheFile(filePath string) bool {
	_, active := activeDiskCacheFiles.Load(filepath.Clean(filePath))
	return active
}

// GetDiskCacheDir resolves the shared disk-cache directory on every call so
// runtime configuration changes take effect.
func GetDiskCacheDir() string {
	cachePath := GetDiskCachePath()
	if cachePath == "" {
		cachePath = os.TempDir()
	}
	return filepath.Join(cachePath, diskCacheDir)
}

// EnsureDiskCacheDir creates the cache directory when needed.
func EnsureDiskCacheDir() error {
	dir := GetDiskCacheDir()
	return os.MkdirAll(dir, 0755)
}

// CreateDiskCacheFile creates a managed body or file cache entry and returns
// its path and open file handle.
func CreateDiskCacheFile(cacheType DiskCacheType) (string, *os.File, error) {
	if err := EnsureDiskCacheDir(); err != nil {
		return "", nil, fmt.Errorf("failed to create cache directory: %w", err)
	}

	dir := GetDiskCacheDir()
	filename := fmt.Sprintf("%s-%s-%d.tmp", cacheType, uuid.New().String()[:8], time.Now().UnixNano())
	filePath := filepath.Join(dir, filename)

	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create cache file: %w", err)
	}

	return filePath, file, nil
}

// WriteDiskCacheFile writes data to a managed cache file and returns its path.
func WriteDiskCacheFile(cacheType DiskCacheType, data []byte) (string, error) {
	if err := reserveDiskCacheBytes(int64(len(data))); err != nil {
		return "", err
	}
	reserved := true
	defer func() {
		if reserved {
			releaseDiskCacheBytes(int64(len(data)))
		}
	}()
	filePath, file, err := CreateDiskCacheFile(cacheType)
	if err != nil {
		return "", err
	}

	registerActiveDiskCacheFile(filePath)
	_, err = file.Write(data)
	if err != nil {
		file.Close()
		os.Remove(filePath)
		unregisterActiveDiskCacheFile(filePath)
		return "", fmt.Errorf("failed to write cache file: %w", err)
	}

	if err := file.Close(); err != nil {
		os.Remove(filePath)
		unregisterActiveDiskCacheFile(filePath)
		return "", fmt.Errorf("failed to close cache file: %w", err)
	}

	incrementDiskFileCount()
	reserved = false
	return filePath, nil
}

// WriteDiskCacheFileString writes a string to a managed cache file.
func WriteDiskCacheFileString(cacheType DiskCacheType, data string) (string, error) {
	if err := reserveDiskCacheBytes(int64(len(data))); err != nil {
		return "", err
	}
	reserved := true
	defer func() {
		if reserved {
			releaseDiskCacheBytes(int64(len(data)))
		}
	}()
	filePath, file, err := CreateDiskCacheFile(cacheType)
	if err != nil {
		return "", err
	}
	registerActiveDiskCacheFile(filePath)
	if _, err = io.WriteString(file, data); err != nil {
		file.Close()
		os.Remove(filePath)
		unregisterActiveDiskCacheFile(filePath)
		return "", fmt.Errorf("failed to write cache file: %w", err)
	}
	if err = file.Close(); err != nil {
		os.Remove(filePath)
		unregisterActiveDiskCacheFile(filePath)
		return "", fmt.Errorf("failed to close cache file: %w", err)
	}
	incrementDiskFileCount()
	reserved = false
	return filePath, nil
}

func ReleaseDiskCacheFileOwnership(filePath string, size int64) {
	unregisterActiveDiskCacheFile(filePath)
	decrementDiskFileCount()
	releaseDiskCacheBytes(size)
}

// ReservedDiskCacheWriter incrementally reserves every byte before it is
// written and transfers the committed reservation to the returned file owner.
type ReservedDiskCacheWriter struct {
	file        *os.File
	filePath    string
	reservation *diskCacheReservation
	written     int64
	committed   bool
}

func NewReservedDiskCacheWriter(cacheType DiskCacheType, initialReservation int64) (*ReservedDiskCacheWriter, error) {
	reservation := &diskCacheReservation{}
	if err := reservation.ensure(initialReservation); err != nil {
		return nil, err
	}
	filePath, file, err := CreateDiskCacheFile(cacheType)
	if err != nil {
		reservation.release()
		return nil, err
	}
	registerActiveDiskCacheFile(filePath)
	return &ReservedDiskCacheWriter{file: file, filePath: filePath, reservation: reservation}, nil
}

func (w *ReservedDiskCacheWriter) Write(data []byte) (int, error) {
	target := w.written + int64(len(data))
	if target < w.written {
		return 0, ErrDiskCacheCapacityExhausted
	}
	if err := w.reservation.ensure(target); err != nil {
		return 0, err
	}
	n, err := w.file.Write(data)
	w.written += int64(n)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return n, err
}

func (w *ReservedDiskCacheWriter) Commit() (string, int64, error) {
	if w.committed {
		return "", 0, fmt.Errorf("disk cache writer already committed")
	}
	if err := w.file.Close(); err != nil {
		w.Abort()
		return "", 0, err
	}
	w.reservation.trimTo(w.written)
	incrementDiskFileCount()
	w.committed = true
	return w.filePath, w.written, nil
}

func (w *ReservedDiskCacheWriter) Abort() {
	if w == nil || w.committed {
		return
	}
	_ = w.file.Close()
	_ = os.Remove(w.filePath)
	unregisterActiveDiskCacheFile(w.filePath)
	w.reservation.release()
	w.committed = true
}

// ReadDiskCacheFile reads a managed cache file.
func ReadDiskCacheFile(filePath string) ([]byte, error) {
	return os.ReadFile(filePath)
}

// ReadDiskCacheFileString reads a managed cache file as a string.
func ReadDiskCacheFileString(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// RemoveDiskCacheFile removes a managed cache file.
func RemoveDiskCacheFile(filePath string) error {
	return os.Remove(filePath)
}

// CleanupOldDiskCacheFiles removes unowned files older than maxAge.
func CleanupOldDiskCacheFiles(maxAge time.Duration) error {
	dir := GetDiskCacheDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // A missing cache directory is already clean.
		}
		return err
	}

	now := time.Now()
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > maxAge {
			filePath := filepath.Join(dir, entry.Name())
			if isActiveDiskCacheFile(filePath) {
				continue
			}
			// Background cleanup only knows the on-disk size. That is exact for
			// the current base64 storage representation.
			if err := os.Remove(filePath); err == nil {
				DecrementDiskFiles(info.Size())
			}
		}
	}
	return nil
}

// GetDiskCacheInfo reports the cache directory and configured limit.
func GetDiskCacheInfo() (fileCount int, totalSize int64, err error) {
	dir := GetDiskCacheDir()

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, err
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		fileCount++
		totalSize += info.Size()
	}
	return fileCount, totalSize, nil
}

// ShouldUseDiskCache reports whether a complete body should use disk storage.
func ShouldUseDiskCache(dataSize int64) bool {
	if !IsDiskCacheEnabled() {
		return false
	}
	threshold := GetDiskCacheThresholdBytes()
	if dataSize < threshold {
		return false
	}
	return true
}
