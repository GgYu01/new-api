package router

import (
	"embed"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

// WebAssets holds the embedded dashboard frontend assets.
type WebAssets struct {
	BuildFS   embed.FS
	IndexPage []byte
}

func SetWebRouter(router *gin.Engine, assets WebAssets, pluginDispatcher gin.HandlerFunc) {
	frontendFS := common.EmbedFolder(assets.BuildFS, "web/dist")

	router.GET("/admin/disguise-routing", controller.ServeDisguiseRoutingAdminPage)
	router.GET("/disguise-routing", controller.ServeDisguiseRoutingAdminPage)

	router.NoRoute(
		pluginDispatcher,
		middleware.RouteTag("web"),
		gzip.Gzip(gzip.DefaultCompression),
		middleware.AccessTokenAudit(),
		middleware.GlobalWebRateLimit(),
		middleware.Cache(),
		func(c *gin.Context) {
			urlPath := c.Request.URL.Path
			if urlPath == "/" || urlPath == "/index.html" {
				c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
				c.Header("Pragma", "no-cache")
				c.Header("Expires", "0")
				c.Data(http.StatusOK, "text/html; charset=utf-8", assets.IndexPage)
				c.Abort()
				return
			}
		},
		static.Serve("/", frontendFS),
		func(c *gin.Context) {
			urlPath := c.Request.URL.Path
			if strings.HasPrefix(urlPath, "/static/") || strings.HasPrefix(urlPath, "/assets/") {
				c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
				c.Header("Pragma", "no-cache")
				c.Header("Expires", "0")
				c.Header("Clear-Site-Data", `"cache"`)
				if strings.HasSuffix(urlPath, ".js") || strings.HasSuffix(urlPath, ".mjs") {
					reloadScript := `(function(){try{var k='__newapi_stale_chunk_reload__';var last=Number(sessionStorage.getItem(k)||'0');if(Date.now()-last>4000){sessionStorage.setItem(k,String(Date.now()));window.location.replace(window.location.href);}}catch(e){window.location.reload();}})();`
					c.Data(http.StatusOK, "application/javascript; charset=utf-8", []byte(reloadScript))
					return
				}
				if strings.HasSuffix(urlPath, ".css") {
					c.Data(http.StatusOK, "text/css; charset=utf-8", []byte("/* stale css chunk fallback */\n"))
					return
				}
				controller.RelayNotFound(c)
				return
			}
			if strings.HasPrefix(c.Request.RequestURI, "/v1") || strings.HasPrefix(c.Request.RequestURI, "/api") {
				c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
				controller.RelayNotFound(c)
				return
			}
			c.Header("Cache-Control", "no-store, no-cache, must-revalidate")
			c.Header("Pragma", "no-cache")
			c.Header("Expires", "0")
			c.Data(http.StatusOK, "text/html; charset=utf-8", assets.IndexPage)
		},
	)
}
