package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/handler"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/service"
)

// 接收客户端提交的问题。
type ResearchRequest struct {
	Query string `json:"query"`
	Mode  string `json:"mode"`
	Fresh bool   `json:"fresh,omitempty"`
}

// 接收 Python 返回的答案，也作为 Go 的响应格式。
type ResearchResponse struct {
	Result string           `json:"result"`
	Mode   string           `json:"mode"`
	Plan   json.RawMessage  `json:"plan,omitempty"`
	Search json.RawMessage  `json:"search,omitempty"`
	TaskID string           `json:"task_id,omitempty"`
	Runs   []model.AgentRun `json:"runs,omitempty"`
}

var taskService *service.TaskService

// 创建一个可以重复使用的 HTTP 客户端。
// 普通问答：Python 120 秒，Go 多留 10 秒通信时间。
var agentClient = &http.Client{
	Timeout: 130 * time.Second,
}

// 完整研究：Python 总预算 300 秒，Go 等待 310 秒。
var researchAgentClient = &http.Client{Timeout: 310 * time.Second}
var agentServiceURL = envOrDefault("AGENT_SERVICE_URL", "http://127.0.0.1:8000/research")

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := gin.Default()
	// 不使用任意客户端传入的 X-Forwarded-For 作为可信身份。
	if err := r.SetTrustedProxies(nil); err != nil {
		log.Fatal(err)
	}
	r.Use(securityHeaders())

	r.GET("/ping", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "pong",
		})
	})

	limits := newRequestLimits(
		envInteger("MAX_CONCURRENT_RESEARCH", 2, 1),
		envInteger("RATE_LIMIT_PER_MINUTE", 20, 0),
	)
	optionalAuth := func(c *gin.Context) {
		if c.GetHeader("Authorization") != "" {
			c.AbortWithStatusJSON(503, gin.H{"error": "未启用用户数据库，无法验证登录凭据"})
			return
		}
		c.Next()
	}
	if dsn := os.Getenv("MYSQL_DSN"); dsn != "" {
		startup, cancel := context.WithTimeout(ctx, 30*time.Second)
		store, err := repository.Open(startup, dsn)
		if err != nil {
			cancel()
			log.Fatal(err)
		}
		defer store.DB.Close()
		if err := store.Migrate(startup); err != nil {
			cancel()
			log.Fatalf("数据库迁移失败 error_type=%T", err)
		}
		cancel()
		cache, err := service.NewTaskCache(os.Getenv("REDIS_URL"), envOrDefault("CACHE_PREFIX", "arp"))
		if err != nil {
			log.Fatal(err)
		}
		defer cache.Close()
		taskService = &service.TaskService{Store: store, Cache: cache}
		accounts, err := handler.NewAccounts(taskService, os.Getenv("JWT_SECRET"))
		if err != nil {
			log.Fatal(err)
		}
		optionalAuth = accounts.Auth(false)
		authLimits := newRequestLimits(2, 20)
		r.POST("/api/auth/register", authLimits.middleware(), accounts.Register)
		r.POST("/api/auth/login", authLimits.middleware(), accounts.Login)
		tasks := r.Group("/api/tasks", accounts.Auth(true))
		tasks.GET("", accounts.ListTasks)
		tasks.GET("/:id", accounts.GetTask)
		tasks.GET("/:id/runs", accounts.GetRuns)
		tasks.GET("/:id/report", accounts.GetReport)
		log.Printf("[startup] mysql=ok redis=%s", cache.Health(ctx))
		go func() {
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				check, cancel := context.WithTimeout(ctx, 10*time.Second)
				taskService.RecoverExpired(check)
				cancel()
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	}
	r.POST("/api/research", optionalAuth, limits.middleware(), handleResearch)
	r.GET("/health", func(c *gin.Context) {
		if taskService == nil {
			c.JSON(200, gin.H{"status": "ok", "mysql": "disabled", "redis": "disabled"})
			return
		}
		check, cancel := context.WithTimeout(c.Request.Context(), time.Second)
		defer cancel()
		if taskService.Store.DB.PingContext(check) != nil {
			c.JSON(503, gin.H{"status": "unavailable", "mysql": "unavailable"})
			return
		}
		c.JSON(200, gin.H{"status": "ok", "mysql": "ok", "redis": taskService.Cache.Health(check)})
	})

	frontend, err := os.OpenRoot(envOrDefault("FRONTEND_DIR", "../frontend"))
	if err != nil {
		log.Printf("未加载前端目录（API 仍可使用）: %v", err)
	} else {
		defer frontend.Close()
		registerFrontend(r, frontend)
	}

	server := &http.Server{
		Addr:              envOrDefault("GO_ADDR", "127.0.0.1:8080"),
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      330 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	log.Printf("服务监听 %s；全局并发上限 %d，每分钟请求上限 %d（0 表示不限制频率）", server.Addr, cap(limits.slots), limits.perMinute)
	drained := make(chan struct{})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		close(drained)
	}()
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-drained
}

func handleResearch(c *gin.Context) {
	// 1. 先限制整个请求体，再解析 JSON，避免超大输入占用内存。
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxRequestBytes)
	requestBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "请求体不能超过 64 KiB", "code": "request_too_large"})
		} else {
			c.JSON(http.StatusBadRequest, gin.H{"error": "读取请求内容失败"})
		}
		return
	}
	var input ResearchRequest
	if err := json.Unmarshal(requestBody, &input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "请求格式错误，请提交包含字符串 query 的 JSON",
		})
		return
	}

	// 2. 校验问题：去掉首尾空白，限制为 1～5000 个字符。
	input.Query = strings.TrimSpace(input.Query)
	input.Mode = strings.TrimSpace(input.Mode)
	if input.Mode == "" {
		input.Mode = "chat"
	}
	if input.Mode != "chat" && input.Mode != "research" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "mode 必须是 chat 或 research"})
		return
	}
	length := utf8.RuneCountInString(input.Query)

	if length == 0 || length > 5000 {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "query 必须是 1～5000 个字符的问题",
		})
		return
	}

	// 匿名请求维持原行为；登录请求必须先落库，再调用有成本的模型服务。
	var task model.Task
	var runs []model.AgentRun
	persisted := false
	failureCode, failureMessage := "upstream_failed", "研究未完成，请重新提交"
	if userID := handler.UserID(c); userID != "" {
		create, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		task, err = taskService.Store.CreateTask(create, userID, input.Query, input.Mode)
		cancel()
		if err != nil {
			c.JSON(503, gin.H{"error": "无法保存任务，研究尚未启动"})
			return
		}
		c.Header("X-Task-ID", task.ID)
		defer func() {
			if persisted {
				return
			}
			finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := taskService.Finish(finish, task.UserID, task.ID, nil, runs, failureCode, failureMessage); err != nil {
				log.Printf("[tasks] save failure task=%s error_type=%T", task.ID, err)
			}
		}()
	}

	// 3. 把 Go 结构体转换为发送给 Python 的 JSON。
	body, err := json.Marshal(input)
	if err != nil {
		log.Printf("编码研究请求失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "构造研究请求失败",
		})
		return
	}

	// 4. 创建发送给 Python 的 HTTP 请求。
	req, err := http.NewRequestWithContext(
		c.Request.Context(),
		http.MethodPost,
		agentServiceURL,
		bytes.NewReader(body),
	)
	if err != nil {
		log.Printf("创建研究请求失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "创建研究请求失败",
		})
		return
	}

	req.Header.Set("Content-Type", "application/json")

	// 5. 发送请求，等待 Python 响应。
	client := agentClient
	if input.Mode == "research" {
		client = researchAgentClient
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			failureCode = "upstream_timeout"
			failureMessage = "等待研究服务超时"
		}
		handleAgentError(c, err)
		return
	}
	defer resp.Body.Close()

	// 6. 检查 Python 返回的 HTTP 状态码。
	if resp.StatusCode != http.StatusOK {
		log.Printf("Python 服务返回异常状态码: %d", resp.StatusCode)
		status := http.StatusBadGateway
		message := "Agent 服务执行失败，请查看 Python 服务日志"
		if resp.StatusCode == http.StatusGatewayTimeout {
			status = http.StatusGatewayTimeout
			message = "Agent 执行超时，请稍后重试"
		}
		var failure struct {
			Detail struct {
				Code    string           `json:"code"`
				Stage   string           `json:"stage"`
				Message string           `json:"message"`
				Runs    []model.AgentRun `json:"runs"`
			} `json:"detail"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&failure); err == nil && failure.Detail.Code != "" {
			failureCode, failureMessage, runs = failure.Detail.Code, failure.Detail.Message, failure.Detail.Runs
			c.JSON(status, gin.H{"error": failure.Detail.Message, "code": failure.Detail.Code, "stage": failure.Detail.Stage})
		} else {
			failureMessage = message
			c.JSON(status, gin.H{"error": message})
		}
		return
	}

	// 7. 把 Python 返回的 JSON 解析成 Go 结构体。
	var output ResearchResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&output); err != nil {
		handleAgentError(c, err)
		return
	}

	if strings.TrimSpace(output.Result) == "" {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "Agent 服务返回了空答案",
		})
		return
	}
	runs = output.Runs
	if task.ID != "" {
		finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := taskService.Finish(finish, task.UserID, task.ID, &model.Report{Result: output.Result, Mode: input.Mode, Plan: output.Plan, Search: output.Search}, runs, "", "")
		cancel()
		if err != nil {
			failureCode = "save_failed"
			failureMessage = "报告保存失败"
			c.JSON(503, gin.H{"error": "报告未能保存，请查询任务状态", "task_id": task.ID})
			return
		}
		persisted = true
		output.TaskID = task.ID
	}

	// 8. 把答案返回给客户端。
	c.JSON(http.StatusOK, output)
}

// 统一处理连接失败、读取失败和通信超时。
func handleAgentError(c *gin.Context, err error) {
	log.Printf("调用 Agent 服务失败: %v", err)

	if errors.Is(err, context.DeadlineExceeded) {
		c.JSON(http.StatusGatewayTimeout, gin.H{
			"error": "等待 Agent 服务响应超时",
		})
		return
	}

	c.JSON(http.StatusBadGateway, gin.H{
		"error": "无法获得 Agent 服务的有效响应，请检查 Python 服务",
	})
}
