package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetTimeoutLadderReturnsContractDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("REQUEST_BODY_NO_PROGRESS_TIMEOUT", "")
	t.Setenv("DOWNSTREAM_WRITE_NO_PROGRESS_TIMEOUT", "")

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/system-info/timeout-ladder", nil)
	GetTimeoutLadder(c)

	require.Equal(t, http.StatusOK, recorder.Code)
	var payload struct {
		Success bool           `json:"success"`
		Data    map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &payload))
	require.True(t, payload.Success)
	require.Equal(t, "10m0s", payload.Data["DOWNSTREAM_WRITE_NO_PROGRESS_TIMEOUT"])
	require.Equal(t, "10m0s", payload.Data["REQUEST_BODY_NO_PROGRESS_TIMEOUT"])
	require.Equal(t, "15s", payload.Data["UPSTREAM_CONNECT_TIMEOUT"])
	require.EqualValues(t, 8, payload.Data["MAX_DISPATCHED_UPSTREAM_ATTEMPTS"])
}
