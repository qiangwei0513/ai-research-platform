package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
)

type Client struct {
	URL        string
	HTTP       *http.Client
	OnProgress func(context.Context, repository.Job, model.Progress) error
}

func (a *Client) Execute(ctx context.Context, job repository.Job) repository.Completion {
	input := struct {
		Query string `json:"query"`
		Mode  string `json:"mode"`
		Fresh bool   `json:"fresh"`
	}{job.Task.Query, job.Task.Mode, job.Fresh}
	body, _ := json.Marshal(input)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.URL, bytes.NewReader(body))
	if err != nil {
		return repository.Completion{Code: "upstream_config", Message: "研究服务地址配置错误"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Task-ID", job.Task.ID)
	req.Header.Set("Accept", "application/x-ndjson")
	client := a.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(req)
	if err != nil {
		code, message := "upstream_unavailable", "研究服务连接失败"
		if errors.Is(err, context.DeadlineExceeded) {
			code, message = "upstream_timeout", "研究服务等待超时"
		}
		return repository.Completion{Code: code, Message: message, Retryable: true}
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusOK && strings.HasPrefix(response.Header.Get("Content-Type"), "application/x-ndjson") {
		return a.readStream(ctx, response.Body, job)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024+1))
	if err != nil {
		return repository.Completion{Code: "upstream_read", Message: "读取研究结果失败", Retryable: true}
	}
	if len(raw) > 2*1024*1024 {
		return repository.Completion{Code: "invalid_result", Message: "研究结果超过大小限制"}
	}
	return decodeReply(response.StatusCode, raw, job)
}

func decodeReply(status int, raw []byte, job repository.Job) repository.Completion {
	if status != http.StatusOK {
		result := repository.Completion{Code: "upstream_failed", Message: "研究服务执行失败", Retryable: status == 429 || status >= 500}
		var failure struct {
			Detail struct {
				Code string           `json:"code"`
				Runs []model.AgentRun `json:"runs"`
			} `json:"detail"`
		}
		if json.Unmarshal(raw, &failure) == nil {
			// Preserve known codes and structured records, never expose raw provider errors.
			switch failure.Detail.Code {
			case "no_sources", "search_failed", "stage_failed", "chat_failed":
				result.Code = failure.Detail.Code
				result.Retryable = false
			case "research_timeout", "chat_timeout":
				result.Code = failure.Detail.Code
			}
			result.Runs = failure.Detail.Runs
		}
		if !validRuns(result.Runs) {
			return repository.Completion{Code: "invalid_result", Message: "研究服务返回无效阶段记录"}
		}
		return result
	}
	var output struct {
		model.Report
		Runs []model.AgentRun `json:"runs"`
	}
	if json.Unmarshal(raw, &output) != nil || strings.TrimSpace(output.Result) == "" || (output.Mode != "" && output.Mode != job.Task.Mode) || !validRuns(output.Runs) {
		return repository.Completion{Code: "invalid_result", Message: "研究服务返回无效结果"}
	}
	output.Mode = job.Task.Mode
	return repository.Completion{Report: &output.Report, Runs: output.Runs}
}

func (a *Client) readStream(ctx context.Context, body io.Reader, job repository.Job) repository.Completion {
	const maxBytes = 2 * 1024 * 1024
	input := &io.LimitedReader{R: body, N: maxBytes + 1}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxBytes)
	progressCount := 0
	invalid := repository.Completion{Code: "invalid_result", Message: "研究服务返回无效事件"}
	for scanner.Scan() {
		if input.N <= 0 {
			return invalid
		}
		var frame struct {
			Type   string          `json:"type"`
			Data   json.RawMessage `json:"data"`
			Status int             `json:"status"`
			Detail json.RawMessage `json:"detail"`
		}
		if json.Unmarshal(scanner.Bytes(), &frame) != nil {
			return invalid
		}
		switch frame.Type {
		case "heartbeat":
			continue
		case "progress":
			progressCount++
			var progress model.Progress
			if progressCount > 64 || json.Unmarshal(frame.Data, &progress) != nil || !progress.ValidFor(job.Task.Mode) {
				return invalid
			}
			if a.OnProgress != nil {
				if err := a.OnProgress(ctx, job, progress); err != nil {
					if errors.Is(err, repository.ErrInvalidProgress) {
						return invalid
					}
					return repository.Completion{Code: "progress_unavailable", Message: "保存执行进度失败", Retryable: true}
				}
			}
		case "result":
			return decodeReply(http.StatusOK, frame.Data, job)
		case "error":
			if frame.Status < 400 || frame.Status > 599 {
				return invalid
			}
			raw, _ := json.Marshal(struct {
				Detail json.RawMessage `json:"detail"`
			}{frame.Detail})
			return decodeReply(frame.Status, raw, job)
		default:
			return invalid
		}
	}
	if input.N <= 0 || errors.Is(scanner.Err(), bufio.ErrTooLong) {
		return invalid
	}
	// EOF without a terminal frame is a truncated attempt, never success.
	return repository.Completion{Code: "upstream_read", Message: "研究进度连接中断", Retryable: true}
}

// Reject malformed stage records before they can repeatedly fail a SQL commit.
func validRuns(runs []model.AgentRun) bool {
	if len(runs) > 100 {
		return false
	}
	for _, run := range runs {
		if run.Stage == "" || utf8.RuneCountInString(run.Stage) > 32 || utf8.RuneCountInString(run.ErrorCode) > 64 ||
			(run.Status != "succeeded" && run.Status != "failed") || run.StartedAt.Year() < 1000 || run.FinishedAt.Year() < 1000 || run.FinishedAt.Before(run.StartedAt) {
			return false
		}
	}
	return true
}
