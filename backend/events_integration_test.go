package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/handler"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
)

func eventServer(f *asyncFixture, configure func(*handler.EventStream)) *httptest.Server {
	s := handler.NewEventStream(f.svc.Store)
	s.PollInterval, s.Heartbeat = 10*time.Millisecond, 30*time.Millisecond
	if configure != nil {
		configure(s)
	}
	r := f.router()
	r.GET("/api/tasks/:id/events", f.auth.Auth(true), s.Serve)
	server := httptest.NewServer(r)
	f.t.Cleanup(server.Close)
	return server
}
func eventRequest(t *testing.T, server *httptest.Server, id, token, cursor string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", server.URL+"/api/tasks/"+id+"/events", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}
func nextFrame(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	var frame strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended: %v frame=%s", err, frame.String())
		}
		frame.WriteString(line)
		if line == "\n" {
			return frame.String()
		}
	}
}
func expectFrame(t *testing.T, reader *bufio.Reader, kind string) string {
	t.Helper()
	for i := 0; i < 50; i++ {
		frame := nextFrame(t, reader)
		if strings.Contains(frame, "event: "+kind+"\n") {
			return frame
		}
	}
	t.Fatal("event not observed: " + kind)
	return ""
}

func TestEventTransactionsProgressFencingAndReplay(t *testing.T) {
	f := asyncServices(t)
	ctx := context.Background()
	task := f.create("private question")
	job, claimed, err := f.svc.Store.ClaimJob(ctx, task.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	for _, p := range []model.Progress{{Stage: "planner", Completed: 0, Total: 1}, {Stage: "planner", Completed: 1, Total: 1}, {Stage: "search", Completed: 0, Total: 2}, {Stage: "search", Completed: 1, Total: 2}} {
		if added, err := f.svc.Store.RecordProgress(ctx, task.ID, job.Token, p); err != nil || !added {
			t.Fatal(added, err)
		}
	}
	if added, err := f.svc.Store.RecordProgress(ctx, task.ID, job.Token, model.Progress{Stage: "search", Completed: 1, Total: 2}); err != nil || added {
		t.Fatal(added, err)
	}
	if _, err := f.svc.Store.RecordProgress(ctx, task.ID, job.Token, model.Progress{Stage: "planner", Completed: 1, Total: 1}); !errors.Is(err, repository.ErrInvalidProgress) {
		t.Fatal(err)
	}
	if _, err := f.svc.Store.RecordProgress(ctx, task.ID, "stale-token", model.Progress{Stage: "writer", Total: 1}); !errors.Is(err, repository.ErrConflict) {
		t.Fatal(err)
	}
	snap, err := f.svc.Store.EventSnapshot(ctx, f.user.ID, task.ID)
	if err != nil || snap.LastEventID != 6 || snap.Progress.Completed != 1 {
		t.Fatal(snap, err)
	}
	// A rejected report transaction cannot publish a successful terminal event.
	bad := repository.Completion{Report: &model.Report{Result: "bad", Plan: json.RawMessage(`not-json`)}}
	if _, err := f.svc.Store.CompleteJob(ctx, task.ID, job.Token, bad, 0); err == nil {
		t.Fatal("invalid report committed")
	}
	if f.task(task.ID).Status != "running" || countRows(t, f, "task_events", task.ID) != 6 {
		t.Fatal("partial commit")
	}
	if _, err := f.svc.Store.CompleteJob(ctx, task.ID, job.Token, success(ctx, job), 0); err != nil {
		t.Fatal(err)
	}
	events, err := f.svc.Store.Events(ctx, f.user.ID, task.ID, 0, math.MaxInt64)
	if err != nil || len(events) != 7 {
		t.Fatal(events, err)
	}
	for i, event := range events {
		if event.ID != int64(i+1) {
			t.Fatal(events)
		}
	}
	if events[6].Type != "succeeded" || countRows(t, f, "reports", task.ID) != 1 {
		t.Fatal(events)
	}
	if _, err := f.svc.Store.RecordProgress(ctx, task.ID, job.Token, model.Progress{Stage: "writer", Total: 1}); !errors.Is(err, repository.ErrConflict) {
		t.Fatal(err)
	}
	server := eventServer(f, nil)
	response := eventRequest(t, server, task.ID, tokenFor(f.user), "3")
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for _, id := range []string{"4", "5", "6", "7"} {
		if !strings.Contains(body, "id: "+id+"\n") {
			t.Fatal(body)
		}
	}
	if strings.Contains(body, "id: 3\n") || !strings.Contains(body, "event: snapshot\n") {
		t.Fatal(body)
	}
	// Authenticated snapshots contain the question, progress frames themselves do not.
	if strings.Contains(strings.Split(body, "event: snapshot")[0], "private question") {
		t.Fatal("progress leaked payload")
	}
}

func TestLiveEventsHeartbeatOwnershipLimitsAndDisconnect(t *testing.T) {
	f := asyncServices(t)
	task := f.create("live")
	server := eventServer(f, func(s *handler.EventStream) { s.MaxPerUser = 1 })
	for _, tc := range []struct {
		token, cursor string
		code          int
	}{
		{"", "", 401}, {tokenFor(f.newUser()), "", 404},
		{tokenFor(f.user), "-1", 400}, {tokenFor(f.user), "999", 409},
	} {
		res := eventRequest(t, server, task.ID, tc.token, tc.cursor)
		res.Body.Close()
		if res.StatusCode != tc.code {
			t.Fatal(res.StatusCode, tc)
		}
	}
	res := eventRequest(t, server, task.ID, tokenFor(f.user), "")
	if res.StatusCode != 200 || res.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatal(res.StatusCode, res.Header)
	}
	reader := bufio.NewReader(res.Body)
	expectFrame(t, reader, "snapshot")
	if frame := nextFrame(t, reader); !strings.Contains(frame, ": heartbeat") {
		t.Fatal(frame)
	}
	extra := eventRequest(t, server, task.ID, tokenFor(f.user), "")
	extra.Body.Close()
	if extra.StatusCode != 429 {
		t.Fatal(extra.StatusCode)
	}
	// The live connection must not reserve a pool connection between polls.
	f.svc.Store.DB.SetMaxOpenConns(1)
	queryCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	if err := f.svc.Store.DB.PingContext(queryCtx); err != nil {
		t.Fatal(err)
	}
	cancel()
	job, claimed, err := f.svc.Store.ClaimJob(context.Background(), task.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	expectFrame(t, reader, "running")
	if _, err := f.svc.Store.RecordProgress(context.Background(), task.ID, job.Token, model.Progress{Stage: "planner", Completed: 1, Total: 1}); err != nil {
		t.Fatal(err)
	}
	if frame := expectFrame(t, reader, "planned"); !strings.Contains(frame, `"completed":1`) {
		t.Fatal(frame)
	}
	res.Body.Close()
	// Disconnect releases capacity, without cancelling the job.
	eventually(t, func() bool {
		probe := eventRequest(t, server, task.ID, tokenFor(f.user), "")
		probe.Body.Close()
		return probe.StatusCode == 200
	})
	if f.task(task.ID).Status != "running" {
		t.Fatal("disconnect cancelled job")
	}
}

func TestRetryResetsProgressAndCancelIsTerminal(t *testing.T) {
	f := asyncServices(t)
	ctx := context.Background()
	task := f.create("retry")
	first, _, err := f.svc.Store.ClaimJob(ctx, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Store.RecordProgress(ctx, task.ID, first.Token, model.Progress{Stage: "writer", Total: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Store.CompleteJob(ctx, task.ID, first.Token, repository.Completion{Code: "upstream_read", Retryable: true}, 0); err != nil {
		t.Fatal(err)
	}
	second, _, err := f.svc.Store.ClaimJob(ctx, task.ID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.Store.RecordProgress(ctx, task.ID, first.Token, model.Progress{Stage: "writer", Completed: 1, Total: 1}); !errors.Is(err, repository.ErrConflict) {
		t.Fatal(err)
	}
	if _, err = f.svc.Store.RecordProgress(ctx, task.ID, second.Token, model.Progress{Stage: "planner", Total: 1}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.svc.Store.EventSnapshot(ctx, f.user.ID, task.ID)
	if err != nil || snapshot.Task.Attempt != 2 || snapshot.Progress.Stage != "planner" {
		t.Fatal(snapshot, err)
	}
	server := eventServer(f, nil)
	res := eventRequest(t, server, task.ID, tokenFor(f.user), strconv.FormatInt(snapshot.LastEventID, 10))
	reader := bufio.NewReader(res.Body)
	expectFrame(t, reader, "snapshot")
	if _, err = f.svc.Store.CancelJob(ctx, f.user.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	expectFrame(t, reader, "cancelled")
	if raw, err := io.ReadAll(reader); err != nil || len(raw) != 0 {
		t.Fatal(string(raw), err)
	}
}

func TestEventConnectionEndsAtJWTExpiry(t *testing.T) {
	f := asyncServices(t)
	task := f.create("expiry")
	server := eventServer(f, nil)
	exp := time.Now().Add(1500 * time.Millisecond)
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Issuer: "ai-research-platform", Audience: jwt.ClaimStrings{"ai-research-platform"}, Subject: f.user.ID, ExpiresAt: jwt.NewNumericDate(exp)}).SignedString([]byte(testSecret))
	res := eventRequest(t, server, task.ID, token, "")
	if res.StatusCode != 200 {
		t.Fatal(res.StatusCode)
	}
	if _, err := io.ReadAll(res.Body); err != nil {
		t.Fatal(err)
	}
	if time.Now().After(exp.Add(time.Second)) {
		t.Fatal("expired stream remained open")
	}
	after := eventRequest(t, server, task.ID, token, "")
	after.Body.Close()
	if after.StatusCode != 401 {
		t.Fatal(after.StatusCode)
	}
}
