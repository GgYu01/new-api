package lifecycle

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestObserveExposesRootAndAttemptFields(t *testing.T) {
	lr := New(context.Background(), "root-obs-1", true, types.RelayFormatOpenAI)
	id, _, err := lr.BeginAttempt()
	require.NoError(t, err)
	require.NotEmpty(t, id)
	lr.EndAttempt(AttemptRecord{ChannelID: 7, TransportPath: "cpa-primary-tls", ErrorClass: "transport_eof_before_headers", StatusCode: 502})

	obs := lr.Observe()
	require.Equal(t, "root-obs-1", obs.RootID)
	require.Equal(t, id, obs.AttemptID)
	require.Equal(t, 0, obs.AttemptIndex)
	require.Equal(t, 1, obs.AttemptCount)
	require.Len(t, obs.Attempts, 1)
	require.Equal(t, 7, obs.Attempts[0].ChannelID)
	require.Equal(t, "cpa-primary-tls", obs.Attempts[0].TransportPath)
	require.Equal(t, "transport_eof_before_headers", obs.Attempts[0].ErrorClass)
	require.Equal(t, 502, obs.Attempts[0].StatusCode)
}

func TestObserveReportsWireBytes(t *testing.T) {
	lr := New(context.Background(), "root-bytes-1", true, types.RelayFormatOpenAI)
	body := `{"a":1}`
	storage, err := common.CreateBodyStorageFromReader(strings.NewReader(body), int64(len(body)), 1024)
	require.NoError(t, err)
	t.Cleanup(func() { _ = storage.Close() })
	lr.BindBody(storage)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	w := lr.AttachWriter(c)
	require.NotNil(t, w)
	_, _ = w.Write([]byte(": PING\n\n"))

	obs := lr.Observe()
	require.Equal(t, int64(len(`{"a":1}`)), obs.UpstreamBytes)
	require.Equal(t, int64(len(": PING\n\n")), obs.DownstreamBytes)
}

func TestObserveNilIsEmpty(t *testing.T) {
	var lr *LogicalRequest
	obs := Observe(lr)
	require.Empty(t, obs.RootID)
	require.Empty(t, obs.Attempts)
}
