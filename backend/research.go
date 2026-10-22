package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/handler"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/observe"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
)

// 接收客户端提交的问题。
type ResearchRequest = handler.ResearchInput

// 接收 Python 返回的答案，也作为 Go 的响应格式。
type ResearchResponse struct {
	Usage  *model.Usage     `json:"usage,omitempty"`
	Result string           `json:"result"`
	Mode   string           `json:"mode"`
	Plan   json.RawMessage  `json:"plan,omitempty"`
	Search json.RawMessage  `json:"search,omitempty"`
	TaskID string           `json:"task_id,omitempty"`
	Runs   []model.AgentRun `json:"runs,omitempty"`
}

// 创建一个可以重复使用的 HTTP 客户端。
// 普通问答：Python 120 秒，Go 多留 10 秒通信时间。
var agentClient = &http.Client{
	Timeout: 130 * time.Second,
}

// 完整研究：Python 总预算 300 秒，Go 等待 310 秒。
var researchAgentClient = &http.Client{Timeout: 310 * time.Second}
var agentServiceURL = envOrDefault("AGENT_SERVICE_URL", "http://127.0.0.1:8000/research")

func handleResearch(c *gin.Context) {
	input, ok := handler.ReadResearch(c)
	if !ok {
		return
	}
	var err error

	// 匿名请求维持原行为；登录请求必须先落库，再调用有成本的模型服务。
	var task model.Task
	var runs []model.AgentRun
	var usage *model.Usage
	persisted := false
	failureCode, failureMessage := "upstream_failed", "研究未完成，请重新提交"
	if userID := handler.UserID(c); userID != "" {
		create, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		task, err = taskService.Store.CreateTask(create, userID, input.Query, input.Mode, repository.TaskOptions{RequestID: observe.From(create).Request, DailyLimit: taskService.MaxDailyTasks})
		cancel()
		if err != nil {
			if errors.Is(err, repository.ErrDailyQuota) {
				c.JSON(429, gin.H{"error": "今日任务额度已用完", "code": "daily_quota"})
				return
			}
			c.JSON(503, gin.H{"error": "无法保存任务，研究尚未启动"})
			return
		}
		c.Header("X-Task-ID", task.ID)
		c.Request = c.Request.WithContext(observe.WithIDs(c.Request.Context(), observe.From(c.Request.Context()).Request, task.ID, 0))
		defer func() {
			if persisted {
				return
			}
			finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := taskService.Finish(finish, task.UserID, task.ID, nil, runs, failureCode, failureMessage, usage); err != nil {
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
		observe.Log(c.Request.Context(), "request_build_failed", "error_type", fmt.Sprintf("%T", err))
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "创建研究请求失败",
		})
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", observe.From(c.Request.Context()).Request)
	req.Header.Set("X-Task-ID", task.ID)

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
				Usage   *model.Usage     `json:"usage"`
				Code    string           `json:"code"`
				Stage   string           `json:"stage"`
				Message string           `json:"message"`
				Runs    []model.AgentRun `json:"runs"`
			} `json:"detail"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&failure); err == nil && failure.Detail.Code != "" {
			failureCode, failureMessage, runs = failure.Detail.Code, failure.Detail.Message, failure.Detail.Runs
			if failure.Detail.Usage.Valid() {
				usage = failure.Detail.Usage
			}
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
	if !output.Usage.Valid() {
		c.JSON(502, gin.H{"error": "研究服务返回无效用量"})
		return
	}
	usage = output.Usage
	if task.ID != "" {
		finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := taskService.Finish(finish, task.UserID, task.ID, &model.Report{Result: output.Result, Mode: input.Mode, Plan: output.Plan, Search: output.Search}, runs, "", "", usage)
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
	observe.Log(c.Request.Context(), "upstream_failed", "error_type", fmt.Sprintf("%T", err))

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
