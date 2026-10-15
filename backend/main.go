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
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// 接收客户端提交的问题。
type ResearchRequest struct {
	Query string `json:"query"`
	Mode  string `json:"mode"`
}

// 接收 Python 返回的答案，也作为 Go 的响应格式。
type ResearchResponse struct {
	Result string          `json:"result"`
	Mode   string          `json:"mode"`
	Plan   json.RawMessage `json:"plan,omitempty"`
	Search json.RawMessage `json:"search,omitempty"`
}

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
	r.POST("/api/research", limits.middleware(), handleResearch)

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
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
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
				Code    string `json:"code"`
				Stage   string `json:"stage"`
				Message string `json:"message"`
			} `json:"detail"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&failure); err == nil && failure.Detail.Code != "" {
			c.JSON(status, gin.H{"error": failure.Detail.Message, "code": failure.Detail.Code, "stage": failure.Detail.Stage})
		} else {
			c.JSON(status, gin.H{"error": message})
		}
		return
	}

	// 7. 把 Python 返回的 JSON 解析成 Go 结构体。
	var output ResearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&output); err != nil {
		handleAgentError(c, err)
		return
	}

	if strings.TrimSpace(output.Result) == "" {
		c.JSON(http.StatusBadGateway, gin.H{
			"error": "Agent 服务返回了空答案",
		})
		return
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
