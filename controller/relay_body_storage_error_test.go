package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestRequestBodyStorageErrorStatusReturnsRetryableServiceUnavailable(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	status, handled := requestBodyStorageErrorStatus(
		c,
		fmt.Errorf("spool admission failed: %w", common.ErrDiskCacheCapacityExhausted),
	)

	require.True(t, handled)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "5", recorder.Header().Get("Retry-After"))
}

func TestRequestBodyStorageErrorStatusReturnsRetryableServiceUnavailableWhenLargeLaneTimesOut(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	status, handled := requestBodyStorageErrorStatus(c, common.ErrLargeBodyAdmissionTimeout)

	require.True(t, handled)
	require.Equal(t, http.StatusServiceUnavailable, status)
	require.Equal(t, "5", recorder.Header().Get("Retry-After"))
}

func TestRequestBodyStorageErrorStatusKeepsOversizeAs413(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	status, handled := requestBodyStorageErrorStatus(c, common.ErrRequestBodyTooLarge)

	require.True(t, handled)
	require.Equal(t, http.StatusRequestEntityTooLarge, status)
	require.Empty(t, recorder.Header().Get("Retry-After"))
}

func TestRequestBodyStorageErrorStatusMapsStallTo408(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	status, handled := requestBodyStorageErrorStatus(
		c,
		fmt.Errorf("disk storage creation failed: %w", common.ErrRequestBodyStalled),
	)

	require.True(t, handled)
	require.Equal(t, http.StatusRequestTimeout, status)
	require.Empty(t, recorder.Header().Get("Retry-After"))
}

func TestRequestBodyStorageErrorStatusMapsTruncationTo400(t *testing.T) {
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	status, handled := requestBodyStorageErrorStatus(
		c,
		fmt.Errorf("disk storage creation failed: %w", common.ErrRequestBodyTruncated),
	)

	require.True(t, handled)
	require.Equal(t, http.StatusBadRequest, status)
}
