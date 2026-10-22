package handler

import (
	"context"
	"crypto/subtle"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/observe"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
	"net/http"
	"sort"
	"strings"
	"time"
)

func ObserveHTTP() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := observe.RequestID(c.GetHeader("X-Request-ID"))
		c.Request = c.Request.WithContext(observe.WithIDs(c.Request.Context(), id, "", 0))
		c.Header("X-Request-ID", id)
		started := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		if route == "/internal/metrics" {
			return
		}
		observe.HTTP(route, c.Request.Method, c.Writer.Status(), time.Since(started))
		if route != "/ping" && route != "/health" {
			observe.Log(c.Request.Context(), "http_request", "route", route, "method", c.Request.Method, "status", c.Writer.Status(), "duration_ms", time.Since(started).Milliseconds())
		}
	}
}

func Metrics(store *repository.Store, token string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if len(token) < 32 {
			c.Status(http.StatusNotFound)
			return
		}
		header := c.GetHeader("Authorization")
		provided := strings.TrimPrefix(header, "Bearer ")
		if !strings.HasPrefix(header, "Bearer ") || subtle.ConstantTimeCompare([]byte(provided), []byte(token)) != 1 {
			c.Status(http.StatusUnauthorized)
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()
		text := observe.Snapshot()
		if store != nil {
			values, err := store.Metrics(ctx)
			if err != nil {
				c.Status(503)
				return
			}
			keys := make([]string, 0, len(values))
			for key := range values {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				text += fmt.Sprintf("%s %g\n", key, values[key])
			}
		}
		c.Data(200, "text/plain; version=0.0.4; charset=utf-8", []byte(text))
	}
}
func (a *Accounts) GetUsage(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if _, err := a.Tasks.Store.Task(ctx, UserID(c), c.Param("id")); err != nil {
		taskError(c, err)
		return
	}
	items, err := a.Tasks.Store.Usage(ctx, UserID(c), c.Param("id"))
	if err != nil {
		serverError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}
