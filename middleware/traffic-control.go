package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// GlobalTrafficControl protects authenticated provider-bound execution after
// TokenAuth and before request dispatch/body materialization. /v1/models is a
// separate router and therefore intentionally does not pass through here.
func GlobalTrafficControl() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, authenticated := c.Get("id"); !authenticated {
			c.Next()
			return
		}
		lease, retryAfter, reason := common.AdmitTrafficRequest(time.Now())
		if lease == nil {
			if retryAfter > 0 {
				c.Header("Retry-After", strconv.FormatInt(int64((retryAfter+time.Second-time.Nanosecond)/time.Second), 10))
			}
			c.Header("X-NewAPI-Traffic-Limit", string(reason))
			c.AbortWithStatus(http.StatusTooManyRequests)
			return
		}
		defer lease.Release()
		c.Next()
	}
}
