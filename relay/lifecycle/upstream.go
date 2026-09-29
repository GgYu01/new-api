package lifecycle

import (
	"context"
	"fmt"
	"net/http"

	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

// FallbackRelayErrorHandler is registered by the service layer to avoid circular dependencies.
var FallbackRelayErrorHandler func(ctx context.Context, resp *http.Response, showBody bool) *types.NewAPIError

func SetFallbackRelayErrorHandler(h func(ctx context.Context, resp *http.Response, showBody bool) *types.NewAPIError) {
	FallbackRelayErrorHandler = h
}

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
	if FallbackRelayErrorHandler != nil {
		return FallbackRelayErrorHandler(c.Request.Context(), resp, showBody)
	}
	return types.NewErrorWithStatusCode(fmt.Errorf("upstream error %d", resp.StatusCode), types.ErrorCodeBadResponseStatusCode, resp.StatusCode)
}
