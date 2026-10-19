package main

import (
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

const maxRequestBytes = 64 * 1024

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
