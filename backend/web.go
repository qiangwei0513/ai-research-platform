package main

import (
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'")
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.Header("Cache-Control", "no-store")
		}
		c.Next()
	}
}

// 只公开首页和 assets 下的静态资源，不把项目目录当成文件服务器。
// os.Root 将文件操作限制在前端目录内，包括防止符号链接逃逸。
func registerFrontend(router *gin.Engine, root *os.Root) {
	home := func(c *gin.Context) { serveFrontendFile(c, root, "index.html") }
	assets := func(c *gin.Context) {
		name := strings.TrimPrefix(c.Param("filepath"), "/")
		if name == "" || path.Clean(name) != name || strings.Contains(name, "\\") {
			c.Status(http.StatusNotFound)
			return
		}
		for _, segment := range strings.Split(name, "/") {
			if strings.HasPrefix(segment, ".") {
				c.Status(http.StatusNotFound)
				return
			}
		}
		switch strings.ToLower(path.Ext(name)) {
		case ".css", ".js", ".mjs", ".svg", ".png", ".jpg", ".jpeg", ".webp", ".ico", ".woff", ".woff2":
			serveFrontendFile(c, root, "assets/"+name)
		default:
			c.Status(http.StatusNotFound)
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		router.Handle(method, "/", home)
		router.Handle(method, "/index.html", home)
		router.Handle(method, "/assets/*filepath", assets)
	}
}

func serveFrontendFile(c *gin.Context, root *os.Root, name string) {
	file, err := root.Open(name)
	if err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		c.Status(http.StatusNotFound)
		return
	}
	// 文件名没有内容 hash，部署更新后让浏览器重新校验版本。
	c.Header("Cache-Control", "no-cache")
	http.ServeContent(c.Writer, c.Request, info.Name(), info.ModTime(), file)
}
