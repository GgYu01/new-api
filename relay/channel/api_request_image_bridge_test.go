package channel

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	common2 "github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/imagebridge"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDoRequestKeepsNativeImageStreamAliveBeforeUpstreamHeaders(t *testing.T) {
	service.InitHttpClient()
	gin.SetMode(gin.TestMode)
	settings := operation_setting.GetGeneralSetting()
	original := *settings
	t.Cleanup(func() { *settings = original })
	settings.PingIntervalEnabled = false
	originalBridgeInterval := nativeImageBridgePingInterval
	nativeImageBridgePingInterval = 10 * time.Millisecond
	t.Cleanup(func() { nativeImageBridgePingInterval = originalBridgeInterval })

	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(80 * time.Millisecond)
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data":[{"b64_json":"YWJj"}]}`))
	}))
	defer upstream.Close()

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	imagebridge.SetContext(ctx, imagebridge.Intent{Envelope: imagebridge.EnvelopeResponses, Stream: true})
	common2.SetContextKey(ctx, constant.ContextKeyIsStream, true)

	request, err := http.NewRequestWithContext(ctx.Request.Context(), http.MethodPost, upstream.URL, http.NoBody)
	require.NoError(t, err)
	response, err := doRequest(ctx, request, &relaycommon.RelayInfo{ChannelMeta: &relaycommon.ChannelMeta{}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = response.Body.Close() })

	assert.Contains(t, recorder.Body.String(), ": PING\n\n")
}
