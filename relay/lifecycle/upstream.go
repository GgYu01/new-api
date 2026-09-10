package lifecycle

import (
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
)

// HandleNon2xx classifies a complete upstream error response before any
// semantic commit. The body is drained with a cap and the connection is
// returned to the pool.
func HandleNon2xx(c *gin.Context, resp *http.Response, showBody bool) *types.NewAPIError {
	if resp == nil {
		return types.NewErrorWithStatusCode(fmt.Errorf("nil upstream response"), types.ErrorCodeBadResponseStatusCode, http.StatusBadGateway)
	}
	lr := FromContext(c)
	if lr != nil && !lr.SemanticCommitted() {
		limit := lr.Config().DiagnosticBodyLimit
		body := DrainAndClose(resp, limit)
		apiErr := ErrorFromStatus(resp, body)
		if showBody && len(body) > 0 {
			apiErr = types.NewErrorWithStatusCode(apiErr.Err, types.ErrorCodeBadResponseStatusCode, resp.StatusCode)
		}
		return apiErr
	}
	return service.RelayErrorHandler(c.Request.Context(), resp, showBody)
}
