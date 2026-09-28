package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGlobalTrafficControlRejectsAfterAuthAndReleasesOnCompletion(t *testing.T) {
	previous := common.GetTrafficControlConfig()
	t.Cleanup(func() { require.NoError(t, common.SetTrafficControlConfig(previous)) })
	cfg := common.DefaultTrafficControlConfig()
	cfg.Mode, cfg.GlobalRPM, cfg.Burst, cfg.MaxActiveRequests = common.TrafficControlModeConcurrency, 1, 1, 1
	require.NoError(t, common.SetTrafficControlConfig(cfg))

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("token_id", int64(1)); c.Next() })
	router.Use(GlobalTrafficControl())
	router.GET("/v1/chat/completions", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	first := httptest.NewRecorder()
	router.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))
	require.Equal(t, http.StatusNoContent, first.Code)
	second := httptest.NewRecorder()
	router.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/v1/chat/completions", nil))
	require.Equal(t, http.StatusNoContent, second.Code)
	require.Equal(t, int64(0), common.GetTrafficControlMetrics().ActiveCurrent)
}
