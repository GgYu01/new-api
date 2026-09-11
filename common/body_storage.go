package common

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// BodyStorage is the replayable request-body storage interface.
type BodyStorage interface {
	io.ReadSeeker
	io.Closer
	// Bytes returns the complete body.
	Bytes() ([]byte, error)
	// Size returns the body size in bytes.
	Size() int64
	// IsDisk reports whether the body is stored on disk.
	IsDisk() bool
	// NewReader returns an independent reader positioned at the start of the
	// stored payload. Each call returns a reader with its own cursor, so
	// callers (e.g. http.Request.GetBody) can replay the body concurrently
	// with, or after, other readers without sharing seek state. Closing the
	// returned reader releases only that reader, never the storage itself;
	// after the storage has been closed, NewReader returns ErrStorageClosed.
	NewReader() (io.ReadCloser, error)
}

// ReplayableBody is an outbound request body that can report its byte size and
// create independent readers for transport-level retries.
type ReplayableBody interface {
	io.Reader
	Size() int64
	NewReader() (io.ReadCloser, error)
}

// ErrStorageClosed reports access after storage has been closed.
var ErrStorageClosed = fmt.Errorf("body storage is closed")

// memoryStorage keeps a replayable request body in memory.
type memoryStorage struct {
	data   []byte
	reader *bytes.Reader
	size   int64
	closed int32
	mu     sync.Mutex
}

func newMemoryStorage(data []byte) *memoryStorage {
	size := int64(len(data))
	IncrementMemoryBuffers(size)
	return &memoryStorage{
		data:   data,
		reader: bytes.NewReader(data),
		size:   size,
	}
}

func (m *memoryStorage) Read(p []byte) (n int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if atomic.LoadInt32(&m.closed) == 1 {
		return 0, ErrStorageClosed
	}
	return m.reader.Read(p)
}

func (m *memoryStorage) Seek(offset int64, whence int) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if atomic.LoadInt32(&m.closed) == 1 {
		return 0, ErrStorageClosed
	}
	return m.reader.Seek(offset, whence)
}

func (m *memoryStorage) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if atomic.CompareAndSwapInt32(&m.closed, 0, 1) {
		DecrementMemoryBuffers(m.size)
	}
	return nil
}

func (m *memoryStorage) Bytes() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if atomic.LoadInt32(&m.closed) == 1 {
		return nil, ErrStorageClosed
	}
	return m.data, nil
}

func (m *memoryStorage) NewReader() (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if atomic.LoadInt32(&m.closed) == 1 {
		return nil, ErrStorageClosed
	}
	// A fresh bytes.Reader over the shared immutable backing array: an
	// independent cursor at zero copy cost. NopCloser keeps Close a no-op, so
	// the storage lifecycle stays owned by whoever holds the storage itself.
	return io.NopCloser(bytes.NewReader(m.data)), nil
}

func (m *memoryStorage) Size() int64 {
	return m.size
}

func (m *memoryStorage) IsDisk() bool {
	return false
}

// diskStorage keeps a replayable request body in an owned temporary file.
type diskStorage struct {
	file        *os.File
	filePath    string
	size        int64
	reservation *diskCacheReservation
	closed      int32
	mu          sync.Mutex
}

type diskCacheReservation struct {
	bytes int64
}

func (r *diskCacheReservation) ensure(total int64) error {
	if total < 0 {
		return ErrDiskCacheCapacityExhausted
	}
	for {
		current := atomic.LoadInt64(&r.bytes)
		if total <= current {
			return nil
		}
		delta := total - current
		if err := reserveDiskCacheBytes(delta); err != nil {
			return err
		}
		if atomic.CompareAndSwapInt64(&r.bytes, current, total) {
			return nil
		}
		releaseDiskCacheBytes(delta)
	}
}

func (r *diskCacheReservation) trimTo(total int64) {
	if total < 0 {
		total = 0
	}
	for {
		current := atomic.LoadInt64(&r.bytes)
		if total >= current {
			return
		}
		if atomic.CompareAndSwapInt64(&r.bytes, current, total) {
			releaseDiskCacheBytes(current - total)
			return
		}
	}
}

func (r *diskCacheReservation) release() {
	releaseDiskCacheBytes(atomic.SwapInt64(&r.bytes, 0))
}

type reservingDiskWriter struct {
	file        *os.File
	reservation *diskCacheReservation
	written     int64
}

func (w *reservingDiskWriter) Write(p []byte) (int, error) {
	target := w.written + int64(len(p))
	if target < w.written {
		return 0, ErrDiskCacheCapacityExhausted
	}
	if err := w.reservation.ensure(target); err != nil {
		return 0, err
	}
	n, err := w.file.Write(p)
	w.written += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

func newDiskStorage(data []byte, cachePath string) (*diskStorage, error) {
	reservation := &diskCacheReservation{}
	if err := reservation.ensure(int64(len(data))); err != nil {
		return nil, err
	}
	// Use the shared cache-directory owner.
	filePath, file, err := CreateDiskCacheFile(DiskCacheTypeBody)
	if err != nil {
		reservation.release()
		return nil, err
	}
	registerActiveDiskCacheFile(filePath)
	removePartial := func() {
		_ = file.Close()
		_ = os.Remove(filePath)
		unregisterActiveDiskCacheFile(filePath)
		reservation.release()
	}

	// Write the initial body.
	n, err := file.Write(data)
	if err != nil {
		removePartial()
		return nil, fmt.Errorf("failed to write to temp file: %w", err)
	}
	if n != len(data) {
		removePartial()
		return nil, io.ErrShortWrite
	}

	// Reset the file offset for replay.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		removePartial()
		return nil, fmt.Errorf("failed to seek temp file: %w", err)
	}

	size := int64(n)
	incrementDiskFileCount()

	return &diskStorage{
		file:        file,
		filePath:    filePath,
		size:        size,
		reservation: reservation,
	}, nil
}

func newDiskStorageFromReader(reader io.Reader, maxBytes int64, initialReservation int64, cachePath string) (*diskStorage, error) {
	return newDiskStorageFromPrefixAndReader(nil, reader, maxBytes, initialReservation, cachePath)
}

// copyBodyBufPool mirrors the 32 KiB staging buffer io.Copy would otherwise
// lease from its own pool, so per-request allocation behavior stays the same.
var copyBodyBufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, 32*1024)
		return &buf
	},
}

// copyInboundBodyToDisk streams an inbound client body into replayable disk
// storage while keeping the failure origin distinguishable in logs: a client
// that stopped sending (stall), a canceled request, a slow/broken client
// connection, and a local disk that refused bytes are different failures and
// must not all be reported as "failed to write to temp file". Wrapped stall
// errors keep their root cause via %w so callers can classify them with
// errors.Is / common.IsRequestBodyStalledError.
func copyInboundBodyToDisk(w io.Writer, r io.Reader) (int64, error) {
	bufp := copyBodyBufPool.Get().(*[]byte)
	buf := *bufp
	defer copyBodyBufPool.Put(bufp)

	var written int64
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			wn, writeErr := w.Write(buf[:n])
			written += int64(wn)
			if writeErr != nil {
				return written, fmt.Errorf("failed to write to temp file: %w", writeErr)
			}
			if wn != n {
				return written, io.ErrShortWrite
			}
		}
		if readErr == io.EOF {
			return written, nil
		}
		if readErr != nil {
			switch {
			case errors.Is(readErr, ErrRequestBodyStalled):
				return written, fmt.Errorf("request body read stalled: %w", readErr)
			case errors.Is(readErr, io.ErrUnexpectedEOF):
				return written, truncatedBodyError(r, readErr)
			case errors.Is(readErr, context.Canceled), errors.Is(readErr, context.DeadlineExceeded):
				return written, fmt.Errorf("request body read canceled: %w", readErr)
			default:
				return written, fmt.Errorf("failed to read request body from client: %w", readErr)
			}
		}
	}
}

func newDiskStorageFromPrefixAndReader(prefix []byte, reader io.Reader, maxBytes int64, initialReservation int64, _ string) (*diskStorage, error) {
	if int64(len(prefix)) > maxBytes || initialReservation > maxBytes {
		return nil, ErrRequestBodyTooLarge
	}
	if initialReservation < int64(len(prefix)) {
		initialReservation = int64(len(prefix))
	}

	reservation := &diskCacheReservation{}
	if err := reservation.ensure(initialReservation); err != nil {
		return nil, err
	}
	filePath, file, err := CreateDiskCacheFile(DiskCacheTypeBody)
	if err != nil {
		reservation.release()
		return nil, err
	}
	registerActiveDiskCacheFile(filePath)
	removePartial := func() {
		_ = file.Close()
		_ = os.Remove(filePath)
		unregisterActiveDiskCacheFile(filePath)
		reservation.release()
	}

	writer := &reservingDiskWriter{file: file, reservation: reservation}
	prefixWritten, err := writer.Write(prefix)
	if err != nil {
		removePartial()
		return nil, fmt.Errorf("failed to write body prefix to temp file: %w", err)
	}
	if prefixWritten != len(prefix) {
		removePartial()
		return nil, io.ErrShortWrite
	}

	// Stream only the remaining permitted bytes plus one sentinel byte. This
	// bounds heap use when Content-Length is missing or incorrect.
	if _, err = copyInboundBodyToDisk(writer, io.LimitReader(reader, maxBytes-writer.written+1)); err != nil {
		removePartial()
		return nil, err
	}
	written := writer.written
	if written > maxBytes {
		removePartial()
		return nil, ErrRequestBodyTooLarge
	}

	if _, err := file.Seek(0, io.SeekStart); err != nil {
		removePartial()
		return nil, fmt.Errorf("failed to seek temp file: %w", err)
	}

	reservation.trimTo(written)
	incrementDiskFileCount()
	return &diskStorage{
		file:        file,
		filePath:    filePath,
		size:        written,
		reservation: reservation,
	}, nil
}

func (d *diskStorage) Read(p []byte) (n int, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if atomic.LoadInt32(&d.closed) == 1 {
		return 0, ErrStorageClosed
	}
	return d.file.Read(p)
}

func (d *diskStorage) Seek(offset int64, whence int) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if atomic.LoadInt32(&d.closed) == 1 {
		return 0, ErrStorageClosed
	}
	return d.file.Seek(offset, whence)
}

func (d *diskStorage) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if atomic.CompareAndSwapInt32(&d.closed, 0, 1) {
		closeErr := d.file.Close()
		removeErr := os.Remove(d.filePath)
		unregisterActiveDiskCacheFile(d.filePath)
		decrementDiskFileCount()
		if d.reservation != nil {
			d.reservation.release()
		}
		if closeErr != nil {
			return closeErr
		}
		if removeErr != nil && !os.IsNotExist(removeErr) {
			return removeErr
		}
	}
	return nil
}

func (d *diskStorage) Bytes() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if atomic.LoadInt32(&d.closed) == 1 {
		return nil, ErrStorageClosed
	}

	// Save the current offset.
	currentPos, err := d.file.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}

	// Seek to the beginning.
	if _, err := d.file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	// Read the complete body.
	data := make([]byte, d.size)
	_, err = io.ReadFull(d.file, data)
	if err != nil {
		return nil, err
	}

	// Restore the previous offset.
	if _, err := d.file.Seek(currentPos, io.SeekStart); err != nil {
		return nil, err
	}

	return data, nil
}

func (d *diskStorage) NewReader() (io.ReadCloser, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if atomic.LoadInt32(&d.closed) == 1 {
		return nil, ErrStorageClosed
	}
	// A separate file descriptor over the same cache file: an independent
	// cursor at zero copy cost. Closing the returned reader closes only that
	// descriptor; the storage keeps owning the primary descriptor and the
	// file's lifetime. Readers opened before Close stay usable even after the
	// file is unlinked, as the descriptor keeps the inode alive.
	file, err := os.Open(d.filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open body cache file for replay: %w", err)
	}
	return file, nil
}

func (d *diskStorage) Size() int64 {
	return d.size
}

func (d *diskStorage) IsDisk() bool {
	return true
}

// CreateBodyStorage selects memory or disk storage for a complete body.
func CreateBodyStorage(data []byte) (BodyStorage, error) {
	size := int64(len(data))
	threshold := GetDiskCacheThresholdBytes()

	// Use disk storage once the configured threshold is reached.
	if IsDiskCacheEnabled() &&
		size >= threshold {
		storage, err := newDiskStorage(data, GetDiskCachePath())
		if err != nil {
			// The body is already complete, so memory fallback preserves it.
			SysError(fmt.Sprintf("failed to create disk storage, falling back to memory: %v", err))
			return newMemoryStorage(data), nil
		}
		return storage, nil
	}

	return newMemoryStorage(data), nil
}

type inboundBodyReader struct {
	r             io.Reader
	declaredBytes int64
	receivedBytes int64
}

func (r *inboundBodyReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.receivedBytes += int64(n)
	return n, err
}

func truncatedBodyError(reader io.Reader, cause error) error {
	if in, ok := reader.(*inboundBodyReader); ok && in != nil {
		return fmt.Errorf("%w: declared_bytes=%d received_bytes=%d: %v", ErrRequestBodyTruncated, in.declaredBytes, in.receivedBytes, cause)
	}
	return fmt.Errorf("%w: %v", ErrRequestBodyTruncated, cause)
}

func wrapInboundBodyReader(reader io.Reader, contentLength int64) io.Reader {
	if reader == nil {
		return reader
	}
	return &inboundBodyReader{r: reader, declaredBytes: contentLength}
}

// CreateBodyStorageFromReader streams a reader into replayable body storage.
func CreateBodyStorageFromReader(reader io.Reader, contentLength int64, maxBytes int64) (BodyStorage, error) {
	reader = wrapInboundBodyReader(reader, contentLength)
	threshold := GetDiskCacheThresholdBytes()
	if maxBytes < 0 {
		return nil, fmt.Errorf("max body size must not be negative")
	}
	if contentLength > maxBytes {
		return nil, ErrRequestBodyTooLarge
	}

	// A known body at or above the threshold streams directly to disk.
	if IsDiskCacheEnabled() &&
		contentLength > 0 &&
		contentLength >= threshold {
		storage, err := newDiskStorageFromReader(reader, maxBytes, contentLength, GetDiskCachePath())
		if err != nil {
			if IsRequestBodyTooLargeError(err) {
				return nil, err
			}
			if errors.Is(err, ErrDiskCacheCapacityExhausted) {
				return nil, err
			}
			// The reader has been consumed, so a memory fallback cannot replay it.
			return nil, fmt.Errorf("disk storage creation failed: %w", err)
		}
		IncrementDiskCacheHits()
		return storage, nil
	}

	// Read only to the in-memory threshold first. Unknown-length and
	// understated bodies can then spill while still being consumed instead of
	// being fully materialized before the disk decision.
	memoryLimit := threshold
	if memoryLimit > maxBytes {
		memoryLimit = maxBytes
	}
	data, err := io.ReadAll(io.LimitReader(reader, memoryLimit+1))
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return nil, truncatedBodyError(reader, err)
		}
		return nil, err
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrRequestBodyTooLarge
	}

	if IsDiskCacheEnabled() &&
		int64(len(data)) > memoryLimit {
		storage, err := newDiskStorageFromPrefixAndReader(data, reader, maxBytes, int64(len(data)), GetDiskCachePath())
		if err != nil {
			if IsRequestBodyTooLargeError(err) {
				return nil, err
			}
			if errors.Is(err, ErrDiskCacheCapacityExhausted) {
				return nil, err
			}
			return nil, fmt.Errorf("disk storage creation failed: %w", err)
		}
		IncrementDiskCacheHits()
		return storage, nil
	}

	// Preserve the memory fallback only when disk caching is disabled. If the
	// configured disk budget is exhausted, returning a retryable admission
	// error prevents concurrent large requests from being fully materialized.
	if int64(len(data)) > memoryLimit {
		buffer := bytes.NewBuffer(data)
		remainingLimit := maxBytes - int64(len(data))
		written, err := io.Copy(buffer, io.LimitReader(reader, remainingLimit+1))
		if err != nil {
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, truncatedBodyError(reader, err)
			}
			return nil, err
		}
		if int64(len(data))+written > maxBytes {
			return nil, ErrRequestBodyTooLarge
		}
		data = buffer.Bytes()
	}

	storage, err := CreateBodyStorage(data)
	if err != nil {
		return nil, err
	}
	// Record the final in-memory storage decision.
	if !storage.IsDisk() {
		IncrementMemoryCacheHits()
	} else {
		IncrementDiskCacheHits()
	}
	return storage, nil
}

type replayableBodyReader struct {
	storage BodyStorage
}

func (r replayableBodyReader) Read(p []byte) (int, error) {
	return r.storage.Read(p)
}

func (r replayableBodyReader) Size() int64 {
	return r.storage.Size()
}

func (r replayableBodyReader) NewReader() (io.ReadCloser, error) {
	return r.storage.NewReader()
}

// NewReplayableBodyReader exposes the replay capabilities of storage without
// exposing io.Closer. This keeps ownership of the storage lifecycle with the
// caller instead of allowing net/http to close it as the request body.
func NewReplayableBodyReader(storage BodyStorage) ReplayableBody {
	return replayableBodyReader{storage: storage}
}

// CleanupOldCacheFiles removes unowned cache remnants left by older processes.
func CleanupOldCacheFiles() {
	// Use the shared cache owner and then reconcile its counters.
	if err := CleanupOldDiskCacheFiles(5 * time.Minute); err == nil {
		// A restarted process begins with zero in-memory counters. Reconcile the
		// recent crash remnants retained by the age gate before new admissions.
		SyncDiskCacheStats()
	}
}
