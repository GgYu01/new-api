package types

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCachedFileDataOpenBase64ReaderSupportsIndependentReadersAndClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.b64")
	require.NoError(t, os.WriteFile(path, []byte("QUJD"), 0o600))
	cached := NewDiskCachedData(path, "application/octet-stream", 3)
	first, err := cached.OpenBase64Reader()
	require.NoError(t, err)
	second, err := cached.OpenBase64Reader()
	require.NoError(t, err)
	firstData, err := io.ReadAll(first)
	require.NoError(t, err)
	secondData, err := io.ReadAll(second)
	require.NoError(t, err)
	require.Equal(t, []byte("QUJD"), firstData)
	require.Equal(t, firstData, secondData)
	require.NoError(t, first.Close())
	require.NoError(t, second.Close())
	require.NoError(t, cached.Close())
	require.NoError(t, cached.Close())
	_, err = cached.OpenBase64Reader()
	require.Error(t, err)
}
