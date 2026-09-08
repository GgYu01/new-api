package middleware

import (
	"context"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

const maxExternalTraceLength = 512

func RequestId() func(c *gin.Context) {
	return func(c *gin.Context) {
		// Always generate a unique internal server execution ID.
		// This execution ID serves as the unique key for pre-consume reservations,
		// database records, audit logs, and internal billing idempotency.
		serverExecId := common.NewRequestId()
		c.Set(common.RequestIdKey, serverExecId)

		ctx := context.WithValue(c.Request.Context(), common.RequestIdKey, serverExecId)
		c.Request = c.Request.WithContext(ctx)

		// Set server execution ID in downstream response headers
		c.Header(common.RequestIdKey, serverExecId)
		c.Header("X-Request-Id", serverExecId)

		// Extract and isolate external client correlation and tracing headers
		var clientReqId string
		if c.Request != nil && c.Request.Header != nil {
			clientReqId = strings.TrimSpace(c.Request.Header.Get("X-Client-Request-Id"))
			if clientReqId == "" {
				clientReqId = strings.TrimSpace(c.Request.Header.Get("X-Request-Id"))
			}
			if len(clientReqId) > maxExternalTraceLength {
				clientReqId = clientReqId[:maxExternalTraceLength]
			}

			traceparent := strings.TrimSpace(c.Request.Header.Get(common.TraceparentHeaderKey))
			if traceparent != "" {
				if len(traceparent) > maxExternalTraceLength {
					traceparent = traceparent[:maxExternalTraceLength]
				}
				c.Set(common.ContextKeyTraceparent, traceparent)
			}

			tracestate := strings.TrimSpace(c.Request.Header.Get(common.TracestateHeaderKey))
			if tracestate != "" {
				if len(tracestate) > maxExternalTraceLength {
					tracestate = tracestate[:maxExternalTraceLength]
				}
				c.Set(common.ContextKeyTracestate, tracestate)
			}
		}

		if clientReqId != "" {
			c.Set(common.ContextKeyExternalRequestId, clientReqId)
			// Echo client's correlation ID in X-Client-Request-Id
			c.Header(common.ClientRequestIdKey, clientReqId)
		} else {
			c.Header(common.ClientRequestIdKey, serverExecId)
		}

		c.Next()
	}
}
