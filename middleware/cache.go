package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

const CurrentFrontendCacheEpoch = "r1315-clean"

func applyCacheEpochPurgeIfNeeded(c *gin.Context) {
	cookieVal, err := c.Cookie("newapi_cache_epoch")
	ccHeader := strings.ToLower(c.GetHeader("Cache-Control"))
	isExplicitReload := strings.Contains(ccHeader, "max-age=0") || strings.Contains(ccHeader, "no-cache")
	if err != nil || cookieVal != CurrentFrontendCacheEpoch || isExplicitReload {
		c.SetCookie("newapi_cache_epoch", CurrentFrontendCacheEpoch, 365*24*3600, "/", "", false, false)
		c.Header("Clear-Site-Data", `"cache"`)
	}
}

func CacheEpochPurge() func(c *gin.Context) {
	return func(c *gin.Context) {
		applyCacheEpochPurgeIfNeeded(c)
		c.Next()
	}
}

func Cache() func(c *gin.Context) {
	return func(c *gin.Context) {
		applyCacheEpochPurgeIfNeeded(c)
		uri := c.Request.RequestURI
		if strings.HasPrefix(uri, "/static/") || strings.HasPrefix(uri, "/assets/") {
			c.Header("Cache-Control", "public, max-age=300, must-revalidate")
		} else {
			c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")
		}
		c.Header("Cache-Version", "r13.15-20261024-clean")
		c.Next()
	}
}
