package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
)

func TestStreamPersistsProgressBeforeResult(t *testing.T) {
	recorded := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/x-ndjson" {
			t.Error("missing negotiation")
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"type":"progress","data":{"stage":"chat","completed":0,"total":1}}`)
		w.(http.Flusher).Flush()
		select {
		case <-recorded:
		case <-r.Context().Done():
			return
		}
		fmt.Fprintln(w, `{"type":"result","data":{"result":"answer","mode":"chat"}}`)
	}))
	defer srv.Close()
	client := Client{URL: srv.URL, OnProgress: func(context.Context, repository.Job, model.Progress) error { close(recorded); return nil }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := client.Execute(ctx, repository.Job{Task: model.Task{Mode: "chat"}})
	if result.Report == nil || result.Report.Result != "answer" {
		t.Fatal(result)
	}
}

func TestStreamFailures(t *testing.T) {
	for _, tc := range []struct {
		name, body, code string
		failure          error
		retry            bool
	}{
		{"truncated", "{\"type\":\"heartbeat\"}\n", "upstream_read", nil, true},
		{"malformed", "not-json\n", "invalid_result", nil, false},
		{"wrong-stage", "{\"type\":\"progress\",\"data\":{\"stage\":\"search\",\"completed\":0,\"total\":2}}\n", "invalid_result", nil, false},
		{"db-failure", "{\"type\":\"progress\",\"data\":{\"stage\":\"chat\",\"completed\":0,\"total\":1}}\n", "progress_unavailable", errors.New("db"), true},
		{"regression", "{\"type\":\"progress\",\"data\":{\"stage\":\"chat\",\"completed\":0,\"total\":1}}\n", "invalid_result", repository.ErrInvalidProgress, false},
		{"failure-frame", "{\"type\":\"error\",\"status\":502,\"detail\":{\"code\":\"no_sources\"}}\n", "no_sources", nil, false},
		{"oversize", strings.Repeat(" ", 2*1024*1024) + "\n", "invalid_result", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Client{OnProgress: func(context.Context, repository.Job, model.Progress) error { return tc.failure }}
			result := c.readStream(context.Background(), strings.NewReader(tc.body), repository.Job{Task: model.Task{Mode: "chat"}})
			if result.Code != tc.code || result.Retryable != tc.retry {
				t.Fatal(result)
			}
		})
	}
}
