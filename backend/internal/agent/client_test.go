package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
)

func TestExecuteValidatesProviderOutput(t *testing.T) {
	cases := []struct {
		name, body, code string
		status           int
		retry            bool
	}{
		{"ok", `{"result":"answer","mode":"chat"}`, "", 200, false},
		{"wrong_mode", `{"result":"answer","mode":"research"}`, "invalid_result", 200, false},
		{"bad_json", `oops`, "invalid_result", 200, false},
		{"malformed_stage", `{"result":"answer","runs":[{"stage":"writer","status":"succeeded"}]}`, "invalid_result", 200, false},
		{"oversized", strings.Repeat("x", 2*1024*1024+1), "invalid_result", 200, false},
		{"rate_limit", `{"detail":"secret"}`, "upstream_failed", 429, true},
		{"bad_request", `{"detail":"secret"}`, "upstream_failed", 422, false},
		{"timeout", `{"detail":{"code":"research_timeout"}}`, "research_timeout", 504, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer srv.Close()
			c := Client{URL: srv.URL}
			result := c.Execute(context.Background(), repository.Job{Task: model.Task{Mode: "chat"}})
			if result.Code != tc.code || result.Retryable != tc.retry {
				t.Fatal(result)
			}
			if tc.code == "" && result.Report == nil {
				t.Fatal("missing report")
			}
			if strings.Contains(result.Message, "secret") {
				t.Fatal("provider leak")
			}
		})
	}
}
