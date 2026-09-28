package imagebridge

import (
	"io"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/require"
)

type copyCountingStorage struct {
	inner          common.BodyStorage
	bytesCalls     int
	newReaderCalls int
}

func (s *copyCountingStorage) Read(p []byte) (int, error) { return s.inner.Read(p) }
func (s *copyCountingStorage) Seek(offset int64, whence int) (int64, error) {
	return s.inner.Seek(offset, whence)
}
func (s *copyCountingStorage) Close() error { return s.inner.Close() }
func (s *copyCountingStorage) Bytes() ([]byte, error) {
	s.bytesCalls++
	return s.inner.Bytes()
}
func (s *copyCountingStorage) Size() int64  { return s.inner.Size() }
func (s *copyCountingStorage) IsDisk() bool { return s.inner.IsDisk() }
func (s *copyCountingStorage) NewReader() (io.ReadCloser, error) {
	s.newReaderCalls++
	return s.inner.NewReader()
}
func (s *copyCountingStorage) SHA256Hex() string { return s.inner.SHA256Hex() }
func (s *copyCountingStorage) Complete() bool    { return s.inner.Complete() }

func TestDetectJSONStorageUsesSingleSpoolNoFullCopy(t *testing.T) {
	// 8 MiB filler on a 1 MiB spill threshold is disk-backed like a 100 MiB-class
	// body. The proof is allocation/copy count (Bytes()==0, NewReader reuse),
	// not wall-clock timing and not a second full in-memory copy.
	const filler = int64(8 << 20)
	previous := common.GetDiskCacheConfig()
	common.SetDiskCacheConfig(common.DiskCacheConfig{
		Enabled:     true,
		ThresholdMB: 1,
		MaxSizeMB:   256,
		Path:        t.TempDir(),
	})
	t.Cleanup(func() { common.SetDiskCacheConfig(previous) })

	prefix := `{"prompt":"edit it","image":"data:image/png;base64,`
	suffix := `"}`
	src := io.MultiReader(
		strings.NewReader(prefix),
		&zeroReader{remaining: filler},
		strings.NewReader(suffix),
	)
	storage, err := common.CreateBodyStorageFromReader(src, -1, 32<<20)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	require.True(t, storage.IsDisk(), "large body must land on sequential disk spool")
	require.True(t, storage.Complete())

	spy := &copyCountingStorage{inner: storage}
	intent, matched, err := DetectJSONStorage("/v1/images/edits", spy)
	require.NoError(t, err)
	require.True(t, matched)
	require.Equal(t, ModeEdit, intent.Mode)
	require.Equal(t, 0, spy.bytesCalls, "pre-scan must not Bytes()-copy a 100MiB-class body")
	require.Equal(t, 1, spy.newReaderCalls, "pre-scan must reuse a single sequential NewReader")

	intent2, matched2, err := DetectJSONStorage("/v1/images/edits", spy)
	require.NoError(t, err)
	require.True(t, matched2)
	require.Equal(t, intent.Mode, intent2.Mode)
	require.Equal(t, 0, spy.bytesCalls)
	require.Equal(t, 2, spy.newReaderCalls, "second pass reuses the same spool via NewReader")
}

type zeroReader struct{ remaining int64 }

func (r *zeroReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = 'A'
	}
	r.remaining -= int64(n)
	return n, nil
}
