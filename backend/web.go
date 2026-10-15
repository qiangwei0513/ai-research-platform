package main

import (
	"log"
	"math"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const maxRequestBytes = 64 * 1024

func envInteger(name string, fallback, minimum int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < minimum {
		log.Printf("%s 配置无效，使用默认值 %d", name, fallback)
		return fallback
	}
	return value
}

// 配额属于整个 Go 进程，覆盖 chat 和 research，所有访问者共同使用。
// 不依赖客户端 IP，因此不会被伪造的代理请求头绕过。
type requestLimits struct {
	slots       chan struct{}
	perMinute   int
	mu          sync.Mutex
	windowStart time.Time
	count       int
}

func newRequestLimits(concurrency, perMinute int) *requestLimits {
	return &requestLimits{
		slots:       make(chan struct{}, concurrency),
		perMinute:   perMinute,
		windowStart: time.Now(),
	}
}

func (limits *requestLimits) allowRate() (bool, int) {
	if limits.perMinute == 0 {
		return true, 0
	}
	limits.mu.Lock()
	defer limits.mu.Unlock()
	now := time.Now()
	if now.Sub(limits.windowStart) >= time.Minute {
		limits.windowStart = now
		limits.count = 0
	}
	if limits.count >= limits.perMinute {
		retry := int(math.Ceil(time.Until(limits.windowStart.Add(time.Minute)).Seconds()))
		return false, max(1, retry)
	}
	limits.count++
	return true, 0
}

func (limits *requestLimits) middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxRequestBytes {
			c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{
				"error": "请求体不能超过 64 KiB", "code": "request_too_large",
			})
			return
		}
		select {
		case limits.slots <- struct{}{}:
			defer func() { <-limits.slots }()
		default:
			c.Header("Retry-After", "10")
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "当前使用人数较多，请稍后重试", "code": "server_busy",
			})
			return
		}
		if allowed, retry := limits.allowRate(); !allowed {
			c.Header("Retry-After", strconv.Itoa(retry))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error": "本分钟请求次数已达到上限，请稍后重试", "code": "rate_limited",
			})
			return
		}
		c.Next()
	}
}

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
