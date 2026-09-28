package service

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/stretchr/testify/require"
)

func TestLargeBase64DiskExhaustionDoesNotFallbackToHeap(t *testing.T) {
	previous := common.GetDiskCacheConfig()
	common.SetDiskCacheConfig(common.DiskCacheConfig{
		Enabled: true, ThresholdMB: 1, MaxSizeMB: 1, Path: t.TempDir(),
	})
	common.ResetDiskCacheUsage()
	t.Cleanup(func() {
		common.SetDiskCacheConfig(previous)
		common.ResetDiskCacheUsage()
	})

	raw := make([]byte, 2<<20)
	encoded := base64.StdEncoding.EncodeToString(raw)
	data, err := loadFromBase64(encoded, "application/octet-stream")
	require.Error(t, err)
	require.Nil(t, data)
	require.ErrorContains(t, err, "disk cache capacity unavailable")
	require.Zero(t, common.GetDiskCacheStats().CurrentDiskUsageBytes)
}

func TestURLLargeBodyStreamsToReservedDiskWithAndWithoutLength(t *testing.T) {
	previousMax := constant.MaxFileDownloadMB
	constant.MaxFileDownloadMB = 64
	t.Cleanup(func() { constant.MaxFileDownloadMB = previousMax })
	for _, omitLength := range []bool{false, true} {
		t.Run(map[bool]string{false: "content_length", true: "chunked"}[omitLength], func(t *testing.T) {
			previous := common.GetDiskCacheConfig()
			common.SetDiskCacheConfig(common.DiskCacheConfig{
				Enabled: true, ThresholdMB: 1, MaxSizeMB: 8, Path: t.TempDir(),
			})
			common.ResetDiskCacheUsage()
			t.Cleanup(func() {
				common.SetDiskCacheConfig(previous)
				common.ResetDiskCacheUsage()
			})
			raw := make([]byte, 2<<20)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/octet-stream")
				if omitLength {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			parsed, err := url.Parse(server.URL)
			require.NoError(t, err)
			fetchSetting := system_setting.GetFetchSetting()
			previousFetch := *fetchSetting
			fetchSetting.EnableSSRFProtection = true
			fetchSetting.AllowPrivateIp = true
			fetchSetting.AllowedPorts = []string{parsed.Port()}
			t.Cleanup(func() { *fetchSetting = previousFetch })
			InitHttpClient()

			cached, err := loadFromURL(nil, server.URL)
			require.NoError(t, err)
			require.True(t, cached.IsDisk())
			require.Equal(t, int64(len(raw)), cached.Size)
			require.Positive(t, cached.DiskSize)
			reader, err := cached.OpenBase64Reader()
			require.NoError(t, err)
			decoded := base64.NewDecoder(base64.StdEncoding, reader)
			count, err := io.Copy(io.Discard, decoded)
			require.NoError(t, err)
			require.NoError(t, reader.Close())
			require.Equal(t, int64(len(raw)), count)
			require.NoError(t, cached.Close())
			require.Zero(t, common.GetDiskCacheStats().CurrentDiskUsageBytes)
		})
	}
}

func TestInvalidBase64StreamingValidationRejects(t *testing.T) {
	data, err := loadFromBase64("QUJD$invalid", "application/octet-stream")
	require.Error(t, err)
	require.Nil(t, data)
}
