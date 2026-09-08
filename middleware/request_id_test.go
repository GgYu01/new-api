package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRequestIdMiddleware_IdentitySeparationAndCorrelation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("duplicate client request id generates distinct server execution ids", func(t *testing.T) {
		r := gin.New()
		r.Use(RequestId())

		var execIDs []string
		var externalIDs []string
		r.GET("/test", func(c *gin.Context) {
			execIDs = append(execIDs, c.GetString(common.RequestIdKey))
			externalIDs = append(externalIDs, c.GetString(common.ContextKeyExternalRequestId))
			c.String(http.StatusOK, "ok")
		})

		sharedClientUUID := "550e8400-e29b-41d4-a716-446655440000"

		// Request 1
		req1 := httptest.NewRequest(http.MethodGet, "/test", nil)
		req1.Header.Set("X-Client-Request-Id", sharedClientUUID)
		w1 := httptest.NewRecorder()
		r.ServeHTTP(w1, req1)
		require.Equal(t, http.StatusOK, w1.Code)

		// Request 2 with same external client UUID
		req2 := httptest.NewRequest(http.MethodGet, "/test", nil)
		req2.Header.Set("X-Client-Request-Id", sharedClientUUID)
		w2 := httptest.NewRecorder()
		r.ServeHTTP(w2, req2)
		require.Equal(t, http.StatusOK, w2.Code)

		require.Len(t, execIDs, 2)
		require.Len(t, externalIDs, 2)

		// Server execution IDs MUST be different
		assert.NotEqual(t, execIDs[0], execIDs[1], "server execution IDs must never collide on duplicate client trace")
		assert.NotEmpty(t, execIDs[0])
		assert.NotEmpty(t, execIDs[1])

		// External IDs MUST preserve the client's correlation ID
		assert.Equal(t, sharedClientUUID, externalIDs[0])
		assert.Equal(t, sharedClientUUID, externalIDs[1])

		// Echo header X-Client-Request-Id echoes the client's correlation ID
		assert.Equal(t, sharedClientUUID, w1.Header().Get("X-Client-Request-Id"))
		assert.Equal(t, sharedClientUUID, w2.Header().Get("X-Client-Request-Id"))

		// Server header returns server execution ID
		assert.Equal(t, execIDs[0], w1.Header().Get(common.RequestIdKey))
		assert.Equal(t, execIDs[1], w2.Header().Get(common.RequestIdKey))
	})

	t.Run("w3c traceparent and tracestate extraction", func(t *testing.T) {
		r := gin.New()
		r.Use(RequestId())

		var capturedTP, capturedTS string
		r.GET("/trace", func(c *gin.Context) {
			capturedTP = c.GetString(common.ContextKeyTraceparent)
			capturedTS = c.GetString(common.ContextKeyTracestate)
			c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/trace", nil)
		req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
		req.Header.Set("tracestate", "rojo=1,congo=2")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", capturedTP)
		assert.Equal(t, "rojo=1,congo=2", capturedTS)
	})

	t.Run("512 byte trace bounded length", func(t *testing.T) {
		r := gin.New()
		r.Use(RequestId())

		var capturedExternal string
		r.GET("/long", func(c *gin.Context) {
			capturedExternal = c.GetString(common.ContextKeyExternalRequestId)
			c.String(http.StatusOK, "ok")
		})

		longTrace := ""
		for i := 0; i < 1000; i++ {
			longTrace += "a"
		}
		req := httptest.NewRequest(http.MethodGet, "/long", nil)
		req.Header.Set("X-Client-Request-Id", longTrace)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		assert.Equal(t, 512, len(capturedExternal))
		assert.Equal(t, 512, len(w.Header().Get("X-Client-Request-Id")))
	})

	t.Run("no header defaults to server execution id", func(t *testing.T) {
		r := gin.New()
		r.Use(RequestId())

		var execID, externalID string
		r.GET("/default", func(c *gin.Context) {
			execID = c.GetString(common.RequestIdKey)
			externalID = c.GetString(common.ContextKeyExternalRequestId)
			c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequest(http.MethodGet, "/default", nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)

		assert.NotEmpty(t, execID)
		assert.Empty(t, externalID)
		assert.Equal(t, execID, w.Header().Get(common.RequestIdKey))
		assert.Equal(t, execID, w.Header().Get("X-Client-Request-Id"))
	})
}
