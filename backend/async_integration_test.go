package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/agent"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/handler"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/queue"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/service"
)

type asyncFixture struct {
	t    *testing.T
	svc  *service.TaskService
	auth *handler.Accounts
	q    *queue.Streams
	user model.User
}

func asyncServices(t *testing.T) *asyncFixture {
	t.Helper()
	if os.Getenv("TEST_QUEUE_REDIS_URL") == "" {
		t.Skip("set TEST_QUEUE_REDIS_URL to an isolated queue Redis")
	}
	svc, auth := testServices(t)
	svc.AsyncEnabled = true
	q, err := queue.New(os.Getenv("TEST_QUEUE_REDIS_URL"), "async-test-"+repository.NewID())
	if err != nil {
		t.Fatal(err)
	}
	if q.Health(context.Background()) != "ok" {
		t.Fatal("queue Redis unavailable")
	}
	t.Cleanup(func() { _ = q.Client.Del(context.Background(), q.Key).Err(); q.Close() })
	f := &asyncFixture{t: t, svc: svc, auth: auth, q: q}
	f.user = f.newUser()
	return f
}
func (f *asyncFixture) newUser() model.User {
	u := makeUser(f.t, f.svc)
	f.t.Cleanup(func() {
		for _, table := range []string{"task_events", "task_event_heads", "task_attempts", "task_outbox", "task_jobs", "agent_runs", "reports"} {
			if _, err := f.svc.Store.DB.Exec("DELETE c FROM "+table+" c JOIN tasks t ON t.id=c.task_id WHERE t.user_id=?", u.ID); err != nil {
				f.t.Error(err)
			}
		}
		_, _ = f.svc.Store.DB.Exec("DELETE FROM tasks WHERE user_id=?", u.ID)
		_, _ = f.svc.Store.DB.Exec("DELETE FROM users WHERE id=?", u.ID)
	})
	return u
}
func tokenFor(u model.User) string {
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Issuer: "ai-research-platform", Subject: u.ID, Audience: jwt.ClaimStrings{"ai-research-platform"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}).SignedString([]byte(testSecret))
	return token
}
func (f *asyncFixture) router() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/tasks", f.auth.Auth(true))
	g.POST("", f.auth.CreateTask)
	g.GET("/:id", f.auth.GetTask)
	g.GET("/:id/report", f.auth.GetReport)
	g.GET("/:id/attempts", f.auth.GetAttempts)
	g.POST("/:id/cancel", f.auth.CancelTask)
	return r
}
func (f *asyncFixture) create(query string) model.Task {
	f.t.Helper()
	v, created, err := f.svc.Store.CreateAsyncTask(context.Background(), f.user.ID, query, "research", true, repository.NewID(), 3)
	if err != nil || !created {
		f.t.Fatal(v, created, err)
	}
	return v
}
func (f *asyncFixture) task(id string) model.Task {
	f.t.Helper()
	v, err := f.svc.Store.Task(context.Background(), f.user.ID, id)
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}
func eventually(t *testing.T, condition func() bool) {
	t.Helper()
	end := time.Now().Add(10 * time.Second)
	for time.Now().Before(end) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("condition not reached within 10s")
}
func (f *asyncFixture) terminal(id string, status string) model.Task {
	f.t.Helper()
	var task model.Task
	eventually(f.t, func() bool { task = f.task(id); return task.Status == status })
	return task
}
func (f *asyncFixture) worker(exec func(context.Context, repository.Job) repository.Completion, configure func(*service.Worker)) func() {
	w := &service.Worker{Tasks: f.svc, Queue: f.q, Execute: exec, Concurrency: 2, Lease: time.Second, Heartbeat: 50 * time.Millisecond, RetryBase: 40 * time.Millisecond, MaintenanceInterval: 20 * time.Millisecond, RepublishAfter: 100 * time.Millisecond}
	if configure != nil {
		configure(w)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case <-done:
			case <-time.After(8 * time.Second):
				f.t.Error("worker did not stop")
			}
		})
	}
	f.t.Cleanup(stop)
	return stop
}
func success(context.Context, repository.Job) repository.Completion {
	return repository.Completion{Report: &model.Report{Result: "# verified report", Mode: "research"}}
}
func execSQL(t *testing.T, f *asyncFixture, query string, args ...any) {
	t.Helper()
	if _, err := f.svc.Store.DB.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}
func countRows(t *testing.T, f *asyncFixture, table, id string) int {
	t.Helper()
	var n int
	if err := f.svc.Store.DB.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE task_id=?", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAsyncAPIAuthorizationIdempotencyAndQuota(t *testing.T) {
	f := asyncServices(t)
	r := f.router()
	token := tokenFor(f.user)
	post := func(key, body, credential string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/tasks", strings.NewReader(body))
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	body := `{"query":"question","mode":"research","fresh":true}`
	key := repository.NewID()
	if w := post(key, body, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	for _, input := range []string{`{"query":" "}`, `{"query":"x","mode":"invalid"}`, `{"query":2}`} {
		if w := post(key, input, token); w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := post("short", body, token); w.Code != 400 {
		t.Fatal(w.Code)
	}
	if w := post(key, `{"query":"`+strings.Repeat("x", 70000)+`"}`, token); w.Code != 413 {
		t.Fatal(w.Code)
	}
	// Concurrent HTTP retries share one transactionally-created task and outbox.
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := post(key, body, token)
			if w.Code != 202 {
				t.Error(w.Code, w.Body.String())
				return
			}
			var out struct {
				TaskID string `json:"task_id"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &out)
			ids <- out.TaskID
		}()
	}
	wg.Wait()
	close(ids)
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("duplicate tasks")
		}
		id = got
	}
	if id == "" {
		t.Fatal("no task")
	}
	if countRows(t, f, "task_jobs", id) != 1 || countRows(t, f, "task_outbox", id) != 1 {
		t.Fatal("acceptance not atomic")
	}
	if w := post(key, `{"query":"different"}`, token); w.Code != 409 {
		t.Fatal(w.Code)
	}
	other := f.newUser()
	for _, suffix := range []string{"", "/attempts", "/report", "/cancel"} {
		method := "GET"
		if suffix == "/cancel" {
			method = "POST"
		}
		if w := request(r, method, "/api/tasks/"+id+suffix, tokenFor(other), ""); w.Code != 404 {
			t.Fatal("ownership", suffix, w.Code)
		}
	}
	f.create("second")
	f.create("third")
	if w := post(repository.NewID(), body, token); w.Code != 429 {
		t.Fatal("quota", w.Code)
	}
	for i := 0; i < 2; i++ {
		if w := request(r, "POST", "/api/tasks/"+id+"/cancel", token, ""); w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := post(repository.NewID(), body, token); w.Code != 202 {
		t.Fatal("cancel did not release quota", w.Code)
	}
	f.svc.AsyncEnabled = false
	if w := post(repository.NewID(), body, token); w.Code != 503 {
		t.Fatal(w.Code)
	}
}

func TestAsyncWorkerDuplicateDeliveryAndConcurrency(t *testing.T) {
	f := asyncServices(t)
	tasks := []model.Task{f.create("one"), f.create("two"), f.create("three")}
	var active, maxActive, calls atomic.Int32
	release := make(chan struct{})
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	defer releaseAll()
	f.worker(func(ctx context.Context, j repository.Job) repository.Completion {
		calls.Add(1)
		n := active.Add(1)
		defer active.Add(-1)
		for {
			old := maxActive.Load()
			if n <= old || maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-release:
			return success(ctx, j)
		case <-ctx.Done():
			return repository.Completion{Code: "stopped"}
		}
	}, nil)
	for i := 0; i < 8; i++ {
		if err := f.q.Publish(context.Background(), tasks[0].ID); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, func() bool { return active.Load() == 2 })
	if calls.Load() != 2 {
		t.Fatal("concurrency not bounded", calls.Load())
	}
	releaseAll()
	for _, task := range tasks {
		f.terminal(task.ID, "succeeded")
		if countRows(t, f, "reports", task.ID) != 1 {
			t.Fatal("missing/duplicate report")
		}
	}
	if calls.Load() != 3 || maxActive.Load() != 2 {
		t.Fatal("duplicate execution or concurrency", calls.Load(), maxActive.Load())
	}
	eventually(t, func() bool {
		p, e := f.q.Client.XPending(context.Background(), f.q.Key, f.q.Group).Result()
		return e == nil && p.Count == 0
	})
}

func TestAsyncHTTPRetriesAndFailureRecords(t *testing.T) {
	for _, tc := range []struct {
		name      string
		failCount int
		failure   string
		status    string
		attempts  int
	}{{"recovers", 1, "temporary", "succeeded", 2}, {"exhausted", 9, "temporary", "failed", 3}, {"no_sources", 9, "no_sources", "failed", 1}, {"invalid_result", 9, "invalid_result", "failed", 1}} {
		t.Run(tc.name, func(t *testing.T) {
			f := asyncServices(t)
			task := f.create("retry")
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var input handler.ResearchInput
				_ = json.NewDecoder(r.Body).Decode(&input)
				if !input.Fresh || r.Header.Get("X-Task-ID") != task.ID {
					t.Error("lost execution context")
				}
				n := int(calls.Add(1))
				w.Header().Set("Content-Type", "application/json")
				if n <= tc.failCount {
					if tc.failure == "invalid_result" {
						fmt.Fprint(w, `{"result":""}`)
						return
					}
					w.WriteHeader(503)
					fmt.Fprintf(w, `{"detail":{"code":%q,"message":"secret provider message"}}`, tc.failure)
					return
				}
				fmt.Fprint(w, `{"result":"report","mode":"research"}`)
			}))
			defer upstream.Close()
			client := &agent.Client{URL: upstream.URL}
			f.worker(client.Execute, nil)
			result := f.terminal(task.ID, tc.status)
			attempts, err := f.svc.Store.Attempts(context.Background(), f.user.ID, task.ID)
			if err != nil || len(attempts) != tc.attempts || int(calls.Load()) != tc.attempts {
				t.Fatal(attempts, err, calls.Load())
			}
			if strings.Contains(result.ErrorMessage, "secret") {
				t.Fatal("provider details exposed")
			}
			if tc.status == "succeeded" && attempts[0].Status != "failed" {
				t.Fatal("failed attempt disappeared")
			}
		})
	}
}

func TestAsyncCancellationStopsHTTPAndFencesResult(t *testing.T) {
	f := asyncServices(t)
	queued := f.create("cancel before delivery")
	if _, err := f.svc.Store.CancelJob(context.Background(), f.user.ID, queued.ID); err != nil {
		t.Fatal(err)
	}
	task := f.create("cancel running")
	started, disconnected := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input handler.ResearchInput
		_ = json.NewDecoder(r.Body).Decode(&input)
		if input.Query == queued.Query {
			t.Error("cancelled queued task executed")
		}
		close(started)
		<-r.Context().Done()
		close(disconnected)
	}))
	defer upstream.Close()
	client := &agent.Client{URL: upstream.URL}
	f.worker(client.Execute, nil)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("not started")
	}
	cancelled, err := f.svc.Store.CancelJob(context.Background(), f.user.ID, task.ID)
	if err != nil || cancelled.Status != "cancelled" {
		t.Fatal(cancelled, err)
	}
	select {
	case <-disconnected:
	case <-time.After(5 * time.Second):
		t.Fatal("running request not cancelled")
	}
	if countRows(t, f, "reports", task.ID) != 0 {
		t.Fatal("cancelled task has report")
	}
	attempts, err := f.svc.Store.Attempts(context.Background(), f.user.ID, task.ID)
	if err != nil || len(attempts) != 1 || attempts[0].Status != "cancelled" {
		t.Fatal(attempts, err)
	}
}

func TestAsyncLeaseRecoveryAndOldWorkerFencing(t *testing.T) {
	f := asyncServices(t)
	task := f.create("crash")
	old, claimed, err := f.svc.Store.ClaimJob(context.Background(), task.ID, time.Minute)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	execSQL(t, f, "UPDATE task_jobs SET lease_until=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE task_id=?", task.ID)
	if renewed, err := f.svc.Store.RenewJob(context.Background(), task.ID, old.Token, time.Minute); err != nil || renewed {
		t.Fatal("resurrected expired lease", err)
	}
	recovered, changed, err := f.svc.Store.RecoverJob(context.Background(), task.ID, 0)
	if err != nil || !changed || recovered.Status != "queued" {
		t.Fatal(recovered, err)
	}
	newer, claimed, err := f.svc.Store.ClaimJob(context.Background(), task.ID, time.Minute)
	if err != nil || !claimed || newer.Token == old.Token {
		t.Fatal(newer, err)
	}
	if _, err := f.svc.Store.CompleteJob(context.Background(), task.ID, old.Token, success(context.Background(), old), 0); !errors.Is(err, repository.ErrConflict) {
		t.Fatal("old worker wrote result", err)
	}
	if _, err := f.svc.Store.CompleteJob(context.Background(), task.ID, newer.Token, success(context.Background(), newer), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Store.CompleteJob(context.Background(), task.ID, newer.Token, success(context.Background(), newer), 0); !errors.Is(err, repository.ErrConflict) {
		t.Fatal("completion not idempotent", err)
	}
	if countRows(t, f, "reports", task.ID) != 1 {
		t.Fatal("duplicate report")
	}
	attempts, _ := f.svc.Store.Attempts(context.Background(), f.user.ID, task.ID)
	if len(attempts) != 2 || attempts[0].ErrorCode != "worker_lost" || attempts[1].Status != "succeeded" {
		t.Fatal(attempts)
	}
}

func TestAsyncGracefulWorkerRestart(t *testing.T) {
	f := asyncServices(t)
	task := f.create("restart")
	stop := f.worker(func(ctx context.Context, j repository.Job) repository.Completion {
		<-ctx.Done()
		return repository.Completion{}
	}, nil)
	f.terminal(task.ID, "running")
	stop()
	if got := f.task(task.ID); got.Status != "queued" || got.ErrorCode != "worker_stopped" {
		t.Fatal(got)
	}
	f.worker(success, nil)
	got := f.terminal(task.ID, "succeeded")
	if got.Attempt != 2 {
		t.Fatal(got)
	}
}

func TestAsyncOutboxSurvivesQueueFailureAndDataLoss(t *testing.T) {
	f := asyncServices(t)
	task := f.create("redis failure")
	broken, _ := queue.New("redis://127.0.0.1:1/0", "unavailable")
	defer broken.Close()
	stop := f.worker(success, func(w *service.Worker) { w.Queue = broken })
	eventually(t, func() bool {
		var future bool
		_ = f.svc.Store.DB.QueryRow("SELECT next_publish_at>UTC_TIMESTAMP(6),claim_token FROM task_outbox WHERE task_id=?", task.ID).Scan(&future, new(string))
		return future
	})
	stop()
	if got := f.task(task.ID); got.Status != "queued" || got.Attempt != 0 {
		t.Fatal(got)
	}
	// Simulate successful XADD followed by total loss of this test stream.
	if err := f.q.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	_ = f.q.Publish(context.Background(), task.ID)
	_ = f.q.Client.Del(context.Background(), f.q.Key).Err()
	execSQL(t, f, "UPDATE task_outbox SET next_publish_at=UTC_TIMESTAMP(6) WHERE task_id=?", task.ID)
	f.worker(success, nil)
	f.terminal(task.ID, "succeeded")
	if countRows(t, f, "task_outbox", task.ID) != 0 {
		t.Fatal("outbox retained after claim")
	}
}

func TestAsyncPendingReclaimAndDeadline(t *testing.T) {
	f := asyncServices(t)
	task := f.create("pending")
	ctx := context.Background()
	if err := f.q.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	_ = f.q.Publish(ctx, task.ID)
	m, err := f.q.Read(ctx, "dead-worker")
	if err != nil {
		t.Fatal(err)
	}
	messages, _, err := f.q.Reclaim(ctx, "replacement", "0-0", 0)
	if err != nil || len(messages) != 1 || messages[0].ID != m.ID {
		t.Fatal(messages, err)
	}
	if err = f.q.Ack(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	pending, _ := f.q.Client.XPending(ctx, f.q.Key, f.q.Group).Result()
	if pending.Count != 0 {
		t.Fatal(pending)
	}
	// DB outbox recreates consumed-but-unclaimed delivery.
	stop := f.worker(success, nil)
	f.terminal(task.ID, "succeeded")
	stop()
	for _, running := range []bool{false, true} {
		expired := f.create("expiry")
		if running {
			_, _, err = f.svc.Store.ClaimJob(ctx, expired.ID, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
		}
		execSQL(t, f, "UPDATE tasks SET deadline_at=UTC_TIMESTAMP(6)-INTERVAL 1 SECOND WHERE id=?", expired.ID)
		result, _, err := f.svc.Store.RecoverJob(ctx, expired.ID, 0)
		if err != nil || result.Status != "failed" || result.ErrorCode != "task_expired" {
			t.Fatal(result, err)
		}
	}
}

func TestAsyncTimeoutAndCancelCompletionRace(t *testing.T) {
	f := asyncServices(t)
	task := f.create("timeout")
	stop := f.worker(func(ctx context.Context, j repository.Job) repository.Completion {
		<-ctx.Done()
		return success(ctx, j)
	}, func(w *service.Worker) { w.ResearchTimeout = 30 * time.Millisecond })
	got := f.terminal(task.ID, "failed")
	if got.Attempt != 3 || got.ErrorCode != "upstream_timeout" {
		t.Fatal(got)
	}
	stop()
	for i := 0; i < 5; i++ {
		task := f.create("race")
		job, ok, err := f.svc.Store.ClaimJob(context.Background(), task.ID, time.Minute)
		if err != nil || !ok {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := f.svc.Store.CancelJob(context.Background(), f.user.ID, task.ID)
			if err != nil && !errors.Is(err, repository.ErrConflict) {
				t.Error(err)
			}
		}()
		go func() {
			defer wg.Done()
			_, err := f.svc.Store.CompleteJob(context.Background(), task.ID, job.Token, success(context.Background(), job), 0)
			if err != nil && !errors.Is(err, repository.ErrConflict) {
				t.Error(err)
			}
		}()
		wg.Wait()
		final := f.task(task.ID)
		reports := countRows(t, f, "reports", task.ID)
		if !((final.Status == "succeeded" && reports == 1) || (final.Status == "cancelled" && reports == 0)) {
			t.Fatal(final, reports)
		}
	}
}
