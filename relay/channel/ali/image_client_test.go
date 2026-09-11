package ali

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/stretchr/testify/require"
)

// Test_updateTask_usesSharedRelayClient is the G007-C003 RED/GREEN lock for the
// Ali image async-task poll. That poll is customer-facing relay traffic (the
// /v1 image request waits on /api/v1/tasks/{id} for up to the logical lifetime).
// A private `&http.Client{}` would bypass the shared relay client (Timeout==0
// unless RELAY_TIMEOUT) and the request phase context.
func Test_updateTask_usesSharedRelayClient(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(thisFile), "image.go"))
	require.NoError(t, err)

	inUpdateTask := false
	brace := 0
	body := []string{}
	sc := bufio.NewScanner(strings.NewReader(string(src)))
	for sc.Scan() {
		line := sc.Text()
		if strings.Contains(line, "func updateTask(") {
			inUpdateTask = true
		}
		if !inUpdateTask {
			continue
		}
		body = append(body, line)
		brace += strings.Count(line, "{") - strings.Count(line, "}")
		if brace <= 0 && len(body) > 1 {
			break
		}
	}
	joined := strings.Join(body, "\n")
	require.Contains(t, joined, "GetHttpClientWithProxySettings",
		"updateTask is customer relay traffic and must reuse the shared relay client")
	require.Contains(t, joined, "NewRequestWithContext",
		"updateTask must attach the request phase context so 30m/60m timers cancel the poll")
	require.NotContains(t, joined, "&http.Client{}",
		"updateTask must not construct a private http.Client")
}

func Test_updateTask_honorsPhaseContextCancel(t *testing.T) {
	service.InitHttpClient()
	require.Equal(t, time.Duration(0), service.GetHttpClient().Timeout)

	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	t.Cleanup(server.Close)

	ctx, cancel := context.WithCancel(context.Background())
	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl: server.URL,
			ApiKey:         "k",
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err, _ := updateTask(ctx, info, "task-1")
		done <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("poll never reached upstream")
	}
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled phase context did not abort the shared-client poll")
	}
}

func Test_updateTask_decodesSucceededStatus(t *testing.T) {
	service.InitHttpClient()
	body, err := json.Marshal(AliResponse{Output: AliOutput{TaskStatus: "SUCCEEDED"}})
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/v1/tasks/task-ok", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(server.Close)

	info := &relaycommon.RelayInfo{
		ChannelMeta: &relaycommon.ChannelMeta{
			ChannelBaseUrl: server.URL,
			ApiKey:         "k",
		},
	}
	resp, err, raw := updateTask(context.Background(), info, "task-ok")
	require.NoError(t, err)
	require.Equal(t, "SUCCEEDED", resp.Output.TaskStatus)
	require.JSONEq(t, string(body), string(raw))
}
