package lifecycle

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteFailureEmitsFailedTerminalWithoutStatusRewrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Set(common.RequestIdKey, "root-term-1")

	lr := New(c.Request.Context(), "root-term-1", true, types.RelayFormatOpenAIResponses)
	Install(c, lr)
	lr.AttachWriter(c)
	lr.MarkHeadersCommitted()

	apiErr := types.NewErrorWithStatusCode(
		fmt.Errorf("upstream service unavailable"),
		types.ErrorCodeBadResponseStatusCode,
		http.StatusServiceUnavailable,
	)
	require.NoError(t, WriteFailure(c, lr, apiErr))

	body := recorder.Body.String()
	require.Contains(t, body, "response.failed")
	require.Contains(t, body, "data: [DONE]")
	require.Equal(t, http.StatusOK, recorder.Code)
	require.True(t, lr.TerminalSent())

	before := recorder.Body.Len()
	require.NoError(t, WriteFailure(c, lr, apiErr))
	require.Equal(t, before, recorder.Body.Len(), "terminal must be sent exactly once")
	require.NotContains(t, strings.ToLower(body), "completed")
}
