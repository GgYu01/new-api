package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

func Cache() func(c *gin.Context) {
	return func(c *gin.Context) {
		uri := c.Request.RequestURI
		if strings.HasPrefix(uri, "/static/") || strings.HasPrefix(uri, "/assets/") {
			c.Header("Cache-Control", "public, max-age=604800")
		} else {
			c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")
		}
		c.Header("Cache-Version", "r13.13-20261024-clean")
		c.Next()
	}
}
