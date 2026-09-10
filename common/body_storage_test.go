package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errDiskSpillStartedTooLate = errors.New("disk spill did not start near the memory threshold")

type spillDeadlineReader struct {
	remaining  int64
	read       int64
	spillLimit int64
	cacheDir   string
}

func (r *spillDeadlineReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	if r.read > r.spillLimit {
		entries, err := os.ReadDir(r.cacheDir)
		if err != nil && !os.IsNotExist(err) {
			return 0, err
		}
		spillStarted := false
		for _, entry := range entries {
			if !entry.IsDir() {
				spillStarted = true
				break
			}
		}
		if !spillStarted {
			return 0, errDiskSpillStartedTooLate
		}
	}

	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	clear(p[:n])
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, nil
}

type fixedSizeZeroReader struct {
	remaining int64
}

type failingAfterReader struct {
	remaining int64
	err       error
}

func (r *failingAfterReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, r.err
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	clear(p[:n])
	r.remaining -= int64(n)
	return n, nil
}

type readCountingReader struct {
	readCalls int
}

func (r *readCountingReader) Read(_ []byte) (int, error) {
	r.readCalls++
	return 0, errors.New("unexpected read")
}

func (r *fixedSizeZeroReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	clear(p[:n])
	r.remaining -= int64(n)
	return n, nil
}

func configureBodyStorageDiskCache(tb testing.TB, thresholdMB, maxSizeMB int) string {
	tb.Helper()
	previous := GetDiskCacheConfig()
	cacheRoot := tb.TempDir()
	SetDiskCacheConfig(DiskCacheConfig{
		Enabled:     true,
		ThresholdMB: thresholdMB,
		MaxSizeMB:   maxSizeMB,
		Path:        cacheRoot,
	})
	tb.Cleanup(func() {
		SetDiskCacheConfig(previous)
	})
	return filepath.Join(cacheRoot, diskCacheDir)
}

func TestNewReplayableBodyReaderKeepsStorageLifecycleWithCaller(t *testing.T) {
	payload := []byte(`{"model":"test-model","input":"hello"}`)
	storage, err := CreateBodyStorage(payload)
	require.NoError(t, err)
	defer storage.Close()

	body := NewReplayableBodyReader(storage)
	assert.EqualValues(t, len(payload), body.Size())
	_, exposesCloser := any(body).(io.Closer)
	assert.False(t, exposesCloser, "the request body must not expose the storage closer")

	req, err := http.NewRequest(http.MethodPost, "https://example.com", body)
	require.NoError(t, err)
	require.NoError(t, req.Body.Close())

	replayBody, err := body.NewReader()
	require.NoError(t, err, "closing the HTTP request body must not close the storage")
	replay, err := io.ReadAll(replayBody)
	require.NoError(t, err)
	require.NoError(t, replayBody.Close())
	assert.Equal(t, payload, replay)

	require.NoError(t, storage.Close())
	_, err = body.NewReader()
	require.ErrorIs(t, err, ErrStorageClosed)
}

func TestCreateBodyStorageFromUnknownLengthSpillsWhileReading(t *testing.T) {
	const (
		thresholdBytes = int64(1 << 20)
		bodyBytes      = int64(8 << 20)
	)
	cacheDir := configureBodyStorageDiskCache(t, 1, 64)
	reader := &spillDeadlineReader{
		remaining:  bodyBytes,
		spillLimit: thresholdBytes + 64<<10,
		cacheDir:   cacheDir,
	}

	storage, err := CreateBodyStorageFromReader(reader, -1, 16<<20)
	require.NoError(t, err)
	defer storage.Close()
	require.True(t, storage.IsDisk())
	require.EqualValues(t, bodyBytes, storage.Size())
}

func TestCreateBodyStorageFromUnknownLengthSpillPreservesBodyAndReleasesFile(t *testing.T) {
	cacheDir := configureBodyStorageDiskCache(t, 1, 64)
	payload := bytes.Repeat([]byte("native-spool-payload\n"), 128*1024)
	statsBefore := GetDiskCacheStats()

	storage, err := CreateBodyStorageFromReader(bytes.NewReader(payload), -1, 8<<20)
	require.NoError(t, err)
	require.True(t, storage.IsDisk())
	require.EqualValues(t, len(payload), storage.Size())

	replay, err := storage.NewReader()
	require.NoError(t, err)
	actual, err := io.ReadAll(replay)
	require.NoError(t, err)
	require.NoError(t, replay.Close())
	require.Equal(t, payload, actual)

	statsActive := GetDiskCacheStats()
	require.Equal(t, statsBefore.ActiveDiskFiles+1, statsActive.ActiveDiskFiles)
	require.Equal(t, statsBefore.CurrentDiskUsageBytes+int64(len(payload)), statsActive.CurrentDiskUsageBytes)
	require.NoError(t, storage.Close())

	statsAfter := GetDiskCacheStats()
	require.Equal(t, statsBefore.ActiveDiskFiles, statsAfter.ActiveDiskFiles)
	require.Equal(t, statsBefore.CurrentDiskUsageBytes, statsAfter.CurrentDiskUsageBytes)
	entries, err := os.ReadDir(cacheDir)
	require.NoError(t, err)
	require.Empty(t, entries)
}

func TestCreateBodyStorageFromUnknownLengthRemovesPartialSpillOnFailure(t *testing.T) {
	cacheDir := configureBodyStorageDiskCache(t, 1, 64)
	readErr := errors.New("source read failed")
	statsBefore := GetDiskCacheStats()

	storage, err := CreateBodyStorageFromReader(&failingAfterReader{remaining: 2 << 20, err: readErr}, -1, 8<<20)
	require.Nil(t, storage)
	require.ErrorIs(t, err, readErr)

	statsAfter := GetDiskCacheStats()
	require.Equal(t, statsBefore.ActiveDiskFiles, statsAfter.ActiveDiskFiles)
	require.Equal(t, statsBefore.CurrentDiskUsageBytes, statsAfter.CurrentDiskUsageBytes)
	entries, readDirErr := os.ReadDir(cacheDir)
	require.NoError(t, readDirErr)
	require.Empty(t, entries)
}

func TestCreateBodyStorageFromUnknownLengthRemovesOversizedPartialSpill(t *testing.T) {
	cacheDir := configureBodyStorageDiskCache(t, 1, 64)
	statsBefore := GetDiskCacheStats()

	storage, err := CreateBodyStorageFromReader(&fixedSizeZeroReader{remaining: 4 << 20}, -1, 2<<20)
	require.Nil(t, storage)
	require.ErrorIs(t, err, ErrRequestBodyTooLarge)

	statsAfter := GetDiskCacheStats()
	require.Equal(t, statsBefore.ActiveDiskFiles, statsAfter.ActiveDiskFiles)
	require.Equal(t, statsBefore.CurrentDiskUsageBytes, statsAfter.CurrentDiskUsageBytes)
	entries, readDirErr := os.ReadDir(cacheDir)
	require.NoError(t, readDirErr)
	require.Empty(t, entries)
}

func TestCreateBodyStorageFromUnknownLengthKeepsMemoryFallbackWhenDiskCacheIsDisabled(t *testing.T) {
	previous := GetDiskCacheConfig()
	SetDiskCacheConfig(DiskCacheConfig{
		Enabled:     false,
		ThresholdMB: 1,
		MaxSizeMB:   0,
		Path:        t.TempDir(),
	})
	t.Cleanup(func() {
		SetDiskCacheConfig(previous)
	})
	payload := bytes.Repeat([]byte("memory-fallback"), 160*1024)

	storage, err := CreateBodyStorageFromReader(bytes.NewReader(payload), -1, 8<<20)
	require.NoError(t, err)
	defer storage.Close()
	require.False(t, storage.IsDisk())
	actual, err := storage.Bytes()
	require.NoError(t, err)
	require.Equal(t, payload, actual)
}

func TestCreateBodyStorageRejectsKnownLargeBodyWhenDiskBudgetIsExhaustedBeforeReading(t *testing.T) {
	configureBodyStorageDiskCache(t, 1, 2)
	firstPayload := bytes.Repeat([]byte("a"), 2<<20)
	first, err := CreateBodyStorageFromReader(bytes.NewReader(firstPayload), int64(len(firstPayload)), 4<<20)
	require.NoError(t, err)
	require.True(t, first.IsDisk())
	defer first.Close()

	reader := &readCountingReader{}
	second, err := CreateBodyStorageFromReader(reader, 2<<20, 4<<20)
	require.Nil(t, second)
	require.ErrorIs(t, err, ErrDiskCacheCapacityExhausted)
	require.Zero(t, reader.readCalls, "an over-budget known body must be rejected before it is buffered")
}

func TestCreateBodyStorageReleasesPartialUnknownBodyReservationWhenBudgetFills(t *testing.T) {
	cacheDir := configureBodyStorageDiskCache(t, 1, 2)
	statsBefore := GetDiskCacheStats()

	storage, err := CreateBodyStorageFromReader(
		&fixedSizeZeroReader{remaining: 4 << 20},
		-1,
		8<<20,
	)

	require.Nil(t, storage)
	require.ErrorIs(t, err, ErrDiskCacheCapacityExhausted)
	statsAfter := GetDiskCacheStats()
	require.Equal(t, statsBefore.ActiveDiskFiles, statsAfter.ActiveDiskFiles)
	require.Equal(t, statsBefore.CurrentDiskUsageBytes, statsAfter.CurrentDiskUsageBytes)
	entries, readDirErr := os.ReadDir(cacheDir)
	require.NoError(t, readDirErr)
	require.Empty(t, entries)
}

func TestCreateBodyStorageTrimsOverstatedKnownLengthReservationToActualBytes(t *testing.T) {
	configureBodyStorageDiskCache(t, 1, 8)
	statsBefore := GetDiskCacheStats()
	const actualBytes = int64(2 << 20)

	storage, err := CreateBodyStorageFromReader(
		&fixedSizeZeroReader{remaining: actualBytes},
		4<<20,
		8<<20,
	)
	require.NoError(t, err)
	require.True(t, storage.IsDisk())
	require.EqualValues(t, actualBytes, storage.Size())
	statsActive := GetDiskCacheStats()
	require.Equal(t, statsBefore.CurrentDiskUsageBytes+actualBytes, statsActive.CurrentDiskUsageBytes)
	require.NoError(t, storage.Close())
	statsAfter := GetDiskCacheStats()
	require.Equal(t, statsBefore.CurrentDiskUsageBytes, statsAfter.CurrentDiskUsageBytes)
}

func TestCreateBodyStorageSupportsSixteenConcurrentLargeBodiesWithinReservedBudget(t *testing.T) {
	const (
		workers   = 16
		bodyBytes = int64(2 << 20)
	)
	configureBodyStorageDiskCache(t, 1, 64)
	statsBefore := GetDiskCacheStats()

	type result struct {
		storage BodyStorage
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			storage, err := CreateBodyStorageFromReader(
				&fixedSizeZeroReader{remaining: bodyBytes},
				bodyBytes,
				4<<20,
			)
			results <- result{storage: storage, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	storages := make([]BodyStorage, 0, workers)
	for got := range results {
		require.NoError(t, got.err)
		require.NotNil(t, got.storage)
		require.True(t, got.storage.IsDisk())
		storages = append(storages, got.storage)
	}
	require.Len(t, storages, workers)

	statsActive := GetDiskCacheStats()
	require.Equal(t, statsBefore.ActiveDiskFiles+workers, statsActive.ActiveDiskFiles)
	require.Equal(t, statsBefore.CurrentDiskUsageBytes+workers*bodyBytes, statsActive.CurrentDiskUsageBytes)

	for _, storage := range storages {
		require.NoError(t, storage.Close())
	}
	statsAfter := GetDiskCacheStats()
	require.Equal(t, statsBefore.ActiveDiskFiles, statsAfter.ActiveDiskFiles)
	require.Equal(t, statsBefore.CurrentDiskUsageBytes, statsAfter.CurrentDiskUsageBytes)
}

func TestSixteenUnknownLargeBodiesKeepHeapBoundedAndReturnToBaseline(t *testing.T) {
	const (
		workers   = 16
		bodyBytes = int64(48 << 20)
	)
	configureBodyStorageDiskCache(t, 1, 1024)
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var peakHeap atomic.Uint64
	peakHeap.Store(before.HeapAlloc)
	stopSampling := make(chan struct{})
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		ticker := time.NewTicker(500 * time.Microsecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var current runtime.MemStats
				runtime.ReadMemStats(&current)
				for {
					peak := peakHeap.Load()
					if current.HeapAlloc <= peak || peakHeap.CompareAndSwap(peak, current.HeapAlloc) {
						break
					}
				}
			case <-stopSampling:
				return
			}
		}
	}()

	type result struct {
		storage BodyStorage
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			storage, err := CreateBodyStorageFromReader(
				&fixedSizeZeroReader{remaining: bodyBytes},
				-1,
				48<<20,
			)
			results <- result{storage: storage, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(stopSampling)
	<-samplerDone

	storages := make([]BodyStorage, 0, workers)
	for got := range results {
		require.NoError(t, got.err)
		require.NotNil(t, got.storage)
		storages = append(storages, got.storage)
	}
	peakGrowth := peakHeap.Load() - before.HeapAlloc
	t.Logf("peak heap growth for sixteen concurrent 48 MiB unknown bodies: %d bytes", peakGrowth)
	require.Less(t, peakGrowth, uint64(96<<20), "the heap must not retain sixteen complete 48 MiB bodies")

	for _, storage := range storages {
		require.NoError(t, storage.Close())
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	t.Logf("heap delta after close and GC: %d bytes", int64(after.HeapAlloc)-int64(before.HeapAlloc))
	require.LessOrEqual(t, after.HeapAlloc, before.HeapAlloc+(16<<20), "heap must return near baseline after all bodies close")
}

func TestCreateBodyStorageRejectsKnownOversizeBeforeReading(t *testing.T) {
	reader := &readCountingReader{}
	storage, err := CreateBodyStorageFromReader(reader, 9<<20, 8<<20)
	require.Nil(t, storage)
	require.ErrorIs(t, err, ErrRequestBodyTooLarge)
	require.Zero(t, reader.readCalls)
}

func BenchmarkCreateBodyStorageFromUnknownLengthLarge(b *testing.B) {
	const bodyBytes = int64(16 << 20)
	configureBodyStorageDiskCache(b, 1, 64)
	b.ReportAllocs()
	b.SetBytes(bodyBytes)
	b.ResetTimer()

	for range b.N {
		storage, err := CreateBodyStorageFromReader(&fixedSizeZeroReader{remaining: bodyBytes}, -1, 32<<20)
		if err != nil {
			b.Fatal(err)
		}
		if !storage.IsDisk() {
			b.Fatal("large unknown-length body did not use disk storage")
		}
		if err := storage.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

type errReader func([]byte) (int, error)

func (f errReader) Read(p []byte) (int, error) { return f(p) }

type failingWriter struct{ limit int }

func (w *failingWriter) Write(p []byte) (int, error) {
	if len(p) > w.limit {
		n := w.limit
		w.limit = 0
		return n, errors.New("disk on fire")
	}
	w.limit -= len(p)
	return len(p), nil
}

func TestCopyInboundBodyToDiskClassifiesFailures(t *testing.T) {
	t.Run("happy path writes everything", func(t *testing.T) {
		var out bytes.Buffer
		n, err := copyInboundBodyToDisk(&out, strings.NewReader("hello world"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if n != int64(len("hello world")) || out.String() != "hello world" {
			t.Fatalf("n=%d out=%q", n, out.String())
		}
	})

	t.Run("stall keeps root cause and says read stalled", func(t *testing.T) {
		_, err := copyInboundBodyToDisk(io.Discard, errReader(func(p []byte) (int, error) {
			return 0, fmt.Errorf("no body data for 10s: %w", ErrRequestBodyStalled)
		}))
		if err == nil || !strings.Contains(err.Error(), "request body read stalled") {
			t.Fatalf("expected stalled classification, got %v", err)
		}
		if !IsRequestBodyStalledError(err) {
			t.Fatalf("stall root cause must survive wrapping: %v", err)
		}
	})

	t.Run("cancel is distinct from disk failure", func(t *testing.T) {
		_, err := copyInboundBodyToDisk(io.Discard, errReader(func(p []byte) (int, error) {
			return 0, context.Canceled
		}))
		if err == nil || !strings.Contains(err.Error(), "request body read canceled") {
			t.Fatalf("expected cancel classification, got %v", err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel root cause must survive wrapping: %v", err)
		}
	})

	t.Run("client connection failure is not reported as disk failure", func(t *testing.T) {
		_, err := copyInboundBodyToDisk(io.Discard, errReader(func(p []byte) (int, error) {
			return 0, errors.New("connection reset by peer")
		}))
		if err == nil || !strings.Contains(err.Error(), "failed to read request body from client") {
			t.Fatalf("expected source-read classification, got %v", err)
		}
		if strings.Contains(err.Error(), "failed to write to temp file") {
			t.Fatalf("source-read failure must not be misattributed to disk: %v", err)
		}
	})

	t.Run("destination write failure stays a write failure", func(t *testing.T) {
		w := &failingWriter{limit: 4}
		_, err := copyInboundBodyToDisk(w, strings.NewReader("0123456789"))
		if err == nil || !strings.Contains(err.Error(), "failed to write to temp file") {
			t.Fatalf("expected write classification, got %v", err)
		}
	})
}

func TestCreateBodyStorageFromReaderTruncatesShortBody(t *testing.T) {
	body := strings.NewReader(`{"partial":true`)
	storage, err := CreateBodyStorageFromReader(body, 64, 1024)
	if storage != nil {
		_ = storage.Close()
	}
	if err == nil || !IsRequestBodyTruncatedError(err) {
		t.Fatalf("want truncated, got storage=%v err=%v", storage, err)
	}
	if !strings.Contains(err.Error(), "declared_bytes=64") || !strings.Contains(err.Error(), "received_bytes=") {
		t.Fatalf("missing byte evidence: %v", err)
	}
}

func TestCreateBodyStorageFromReaderUnknownLengthEOFIsNotTruncated(t *testing.T) {
	body := strings.NewReader(`{"ok":true}`)
	storage, err := CreateBodyStorageFromReader(body, -1, 1024)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
}
