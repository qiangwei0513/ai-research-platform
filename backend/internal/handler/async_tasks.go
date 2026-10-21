package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
)

type ResearchInput struct {
	Query string `json:"query"`
	Mode  string `json:"mode"`
	Fresh bool   `json:"fresh,omitempty"`
}

func ReadResearch(c *gin.Context) (ResearchInput, bool) {
	var input ResearchInput
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 64*1024)
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(413, gin.H{"error": "请求体不能超过 64 KiB", "code": "request_too_large"})
		} else {
			c.JSON(400, gin.H{"error": "读取请求内容失败"})
		}
		return input, false
	}
	if json.Unmarshal(body, &input) != nil {
		c.JSON(400, gin.H{"error": "请求格式错误，请提交包含字符串 query 的 JSON"})
		return input, false
	}
	input.Query = strings.TrimSpace(input.Query)
	input.Mode = strings.TrimSpace(input.Mode)
	if input.Mode == "" {
		input.Mode = "chat"
	}
	if input.Mode != "chat" && input.Mode != "research" {
		c.JSON(400, gin.H{"error": "mode 必须是 chat 或 research"})
		return input, false
	}
	if length := utf8.RuneCountInString(input.Query); length == 0 || length > 5000 {
		c.JSON(400, gin.H{"error": "query 必须是 1～5000 个字符的问题"})
		return input, false
	}
	return input, true
}

var requestKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func (a *Accounts) CreateTask(c *gin.Context) {
	if !a.Tasks.AsyncEnabled {
		c.JSON(503, gin.H{"error": "异步任务尚未启用，请配置队列并启动 Worker", "code": "async_disabled"})
		return
	}
	input, ok := ReadResearch(c)
	if !ok {
		return
	}
	key := c.GetHeader("Idempotency-Key")
	if key == "" {
		key = repository.NewID()
	}
	if !requestKey.MatchString(key) {
		c.JSON(400, gin.H{"error": "Idempotency-Key 需为 16～128 位字母、数字、下划线或连字符"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	limit := a.Tasks.MaxActiveTasks
	if limit < 1 {
		limit = 3
	}
	task, created, err := a.Tasks.Store.CreateAsyncTask(ctx, UserID(c), input.Query, input.Mode, input.Fresh, key, limit)
	if errors.Is(err, repository.ErrConflict) {
		c.JSON(409, gin.H{"error": "该 Idempotency-Key 已用于不同的研究请求"})
		return
	}
	if errors.Is(err, repository.ErrTooManyTasks) {
		c.Header("Retry-After", "5")
		c.JSON(429, gin.H{"error": "进行中或排队中的任务过多，请等待完成或取消任务", "code": "active_task_limit"})
		return
	}
	if err != nil {
		serverError(c, err)
		return
	}
	c.Header("Location", "/api/tasks/"+task.ID)
	c.Header("Idempotency-Key", key)
	c.JSON(http.StatusAccepted, gin.H{"task_id": task.ID, "status": task.Status, "created": created, "task": task})
}
func (a *Accounts) CancelTask(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	task, err := a.Tasks.Store.CancelJob(ctx, UserID(c), c.Param("id"))
	if errors.Is(err, repository.ErrConflict) {
		c.JSON(409, gin.H{"error": "任务已结束，不能取消"})
		return
	}
	if err != nil {
		taskError(c, err)
		return
	}
	a.Tasks.CacheTask(ctx, task)
	c.JSON(200, task)
}
func (a *Accounts) GetAttempts(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	defer cancel()
	if _, err := a.Tasks.Store.Task(ctx, UserID(c), c.Param("id")); err != nil {
		taskError(c, err)
		return
	}
	items, err := a.Tasks.Store.Attempts(ctx, UserID(c), c.Param("id"))
	if err != nil {
		taskError(c, err)
		return
	}
	c.JSON(200, gin.H{"items": items})
}
