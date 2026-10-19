package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/handler"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/service"
	"github.com/redis/go-redis/v9"
)

const testSecret = "integration-only-secret-at-least-32-bytes"

func testServices(t *testing.T) (*service.TaskService, *handler.Accounts) {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	url := os.Getenv("TEST_REDIS_URL")
	if dsn == "" || url == "" {
		t.Skip("set TEST_MYSQL_DSN and TEST_REDIS_URL to isolated test services")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store, err := repository.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.DB.Close() })
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatalf("migration not idempotent: %v", err)
	}
	cache, err := service.NewTaskCache(url, "test-"+repository.NewID())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Close)
	if cache.Health(ctx) != "ok" {
		t.Fatal("test Redis unavailable")
	}
	svc := &service.TaskService{Store: store, Cache: cache}
	auth, err := handler.NewAccounts(svc, testSecret)
	if err != nil {
		t.Fatal(err)
	}
	return svc, auth
}

func request(router http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, r)
	return w
}
func makeUser(t *testing.T, svc *service.TaskService) model.User {
	t.Helper()
	u, err := svc.Store.CreateUser(context.Background(), repository.NewID()+"@example.test", "unused-test-hash")
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestAccountsAndResearchPersistence(t *testing.T) {
	svc, auth := testServices(t)
	gin.SetMode(gin.TestMode)
	oldService, oldURL := taskService, agentServiceURL
	taskService = svc
	t.Cleanup(func() { taskService = oldService; agentServiceURL = oldURL })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var input ResearchRequest
		_ = json.NewDecoder(r.Body).Decode(&input)
		w.Header().Set("Content-Type", "application/json")
		if input.Query == "fail" {
			w.WriteHeader(502)
			_, _ = w.Write([]byte(`{"detail":{"code":"search_failed","stage":"search","message":"test search failed"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(ResearchResponse{Result: "# Report", Mode: input.Mode, Plan: json.RawMessage(`{"goal":"test"}`), Search: json.RawMessage(`{"sources":[]}`), Runs: []model.AgentRun{{Stage: "writer", Status: "succeeded", StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}}})
	}))
	defer upstream.Close()
	agentServiceURL = upstream.URL
	r := gin.New()
	r.POST("/api/auth/register", auth.Register)
	r.POST("/api/auth/login", auth.Login)
	r.POST("/api/research", auth.Auth(false), handleResearch)
	r.GET("/api/tasks", auth.Auth(true), auth.ListTasks)
	r.GET("/api/tasks/:id", auth.Auth(true), auth.GetTask)
	r.GET("/api/tasks/:id/report", auth.Auth(true), auth.GetReport)
	r.GET("/api/tasks/:id/runs", auth.Auth(true), auth.GetRuns)
	email := repository.NewID() + "@example.test"
	credentials := `{"email":"` + email + `","password":"test-password-123"}`
	if w := request(r, "POST", "/api/auth/register", "", credentials); w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := request(r, "POST", "/api/auth/register", "", credentials); w.Code != 409 {
		t.Fatal("duplicate user accepted", w.Code)
	}
	u, err := svc.Store.UserByEmail(context.Background(), email)
	if err != nil || u.PasswordHash == "test-password-123" {
		t.Fatal("password not hashed", err)
	}
	w := request(r, "POST", "/api/auth/login", "", credentials)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var login struct {
		Token string `json:"access_token"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &login)
	if w := request(r, "POST", "/api/auth/login", "", strings.Replace(credentials, "test-password-123", "wrong-password", 1)); w.Code != 401 {
		t.Fatal("wrong password accepted")
	}
	if w := request(r, "GET", "/api/tasks", "", ""); w.Code != 401 {
		t.Fatal("anonymous history accessible")
	}
	if w := request(r, "POST", "/api/research", "invalid", `{"query":"x"}`); w.Code != 401 {
		t.Fatal("invalid token downgraded to anonymous")
	}
	w = request(r, "POST", "/api/research", login.Token, `{"query":"test","mode":"research","fresh":true}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result ResearchResponse
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if result.TaskID == "" {
		t.Fatal("missing task ID")
	}
	for _, suffix := range []string{"", "/report", "/runs"} {
		if w := request(r, "GET", "/api/tasks/"+result.TaskID+suffix, login.Token, ""); w.Code != 200 {
			t.Fatal(suffix, w.Code, w.Body.String())
		}
	}
	other := makeUser(t, svc)
	otherToken, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Issuer: "ai-research-platform", Subject: other.ID, Audience: jwt.ClaimStrings{"ai-research-platform"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}).SignedString([]byte(testSecret))
	for _, suffix := range []string{"", "/report", "/runs"} {
		if w := request(r, "GET", "/api/tasks/"+result.TaskID+suffix, otherToken, ""); w.Code != 404 {
			t.Fatal("cross-user access", suffix, w.Code)
		}
	}
	if w := request(r, "GET", "/api/tasks?page_size=0", login.Token, ""); w.Code != 400 {
		t.Fatal("invalid pagination accepted")
	}
	w = request(r, "POST", "/api/research", login.Token, `{"query":"fail","mode":"research"}`)
	if w.Code != 502 {
		t.Fatal(w.Code, w.Body.String())
	}
	failedID := w.Header().Get("X-Task-ID")
	failed, err := svc.Store.Task(context.Background(), u.ID, failedID)
	if err != nil || failed.Status != "failed" || failed.ErrorCode != "search_failed" {
		t.Fatal(failed, err)
	}
	items, total, err := svc.Store.Tasks(context.Background(), u.ID, 1, 1)
	if err != nil || total != 2 || len(items) != 1 {
		t.Fatal("pagination", total, err)
	}
	// 重新打开连接，验证记录来自 MySQL 持久化而非进程内存。
	reopened, err := repository.Open(context.Background(), os.Getenv("TEST_MYSQL_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.DB.Close()
	if report, err := reopened.Report(context.Background(), u.ID, result.TaskID); err != nil || report.Result != "# Report" {
		t.Fatal("report missing after reconnect", err)
	}
	w = request(r, "POST", "/api/research", "", `{"query":"anonymous"}`)
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var anonymous ResearchResponse
	_ = json.Unmarshal(w.Body.Bytes(), &anonymous)
	if anonymous.TaskID != "" {
		t.Fatal("anonymous result persisted")
	}
}

func TestTransactionRollbackAndRecovery(t *testing.T) {
	svc, _ := testServices(t)
	ctx := context.Background()
	u := makeUser(t, svc)
	task, err := svc.Store.CreateTask(ctx, u.ID, "test", "research")
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Store.Finish(ctx, u.ID, task.ID, &model.Report{Result: "test", Plan: json.RawMessage(`invalid json`)}, nil, "", "")
	if err == nil {
		t.Fatal("invalid report unexpectedly committed")
	}
	stored, err := svc.Store.Task(ctx, u.ID, task.ID)
	if err != nil || stored.Status != "running" {
		t.Fatal("partial transaction committed", stored, err)
	}
	if _, err := svc.Store.Report(ctx, u.ID, task.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("report exists after rollback", err)
	}
	if _, err := svc.Store.DB.ExecContext(ctx, "UPDATE tasks SET deadline_at=? WHERE id=?", time.Now().Add(-time.Minute), task.ID); err != nil {
		t.Fatal(err)
	}
	svc.RecoverExpired(ctx)
	stored, err = svc.Store.Task(ctx, u.ID, task.ID)
	if err != nil || stored.Status != "failed" || stored.ErrorCode != "execution_interrupted" {
		t.Fatal("expired task not recovered", stored, err)
	}
	if err := svc.Finish(ctx, u.ID, task.ID, &model.Report{Result: "late response"}, nil, "", ""); !errors.Is(err, repository.ErrConflict) {
		t.Fatal("terminal status overwritten", err)
	}
}

func TestTaskCacheFallbackAndExpiration(t *testing.T) {
	svc, _ := testServices(t)
	ctx := context.Background()
	u := makeUser(t, svc)
	task, err := svc.Store.CreateTask(ctx, u.ID, "initial", "chat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Get(ctx, u.ID, task.ID); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Store.DB.ExecContext(ctx, "UPDATE tasks SET query='changed-in-db' WHERE id=?", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	hit, err := svc.Get(ctx, u.ID, task.ID)
	if err != nil || hit.Query != "initial" {
		t.Fatal("cache was not hit", hit, err)
	}
	time.Sleep(5100 * time.Millisecond)
	expired, err := svc.Get(ctx, u.ID, task.ID)
	if err != nil || expired.Query != "changed-in-db" {
		t.Fatal("cache did not expire", expired, err)
	}
	if err := svc.Finish(ctx, u.ID, task.ID, &model.Report{Result: "saved"}, nil, "", ""); err != nil {
		t.Fatal(err)
	}
	final, err := svc.Get(ctx, u.ID, task.ID)
	if err != nil || final.Status != "succeeded" {
		t.Fatal("cache not updated on completion", final, err)
	}
	broken, err := service.NewTaskCache("redis://127.0.0.1:1/0", "test-broken")
	if err != nil {
		t.Fatal(err)
	}
	defer broken.Close()
	fallback := &service.TaskService{Store: svc.Store, Cache: broken}
	started := time.Now()
	got, err := fallback.Get(ctx, u.ID, task.ID)
	if err != nil || got.Status != "succeeded" {
		t.Fatal("fallback failed", got, err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("Redis degradation blocked database reads")
	}
	if _, err := fallback.Store.Report(ctx, u.ID, task.ID); err != nil {
		t.Fatal("report lost without Redis", err)
	}
	if _, err := fallback.Get(ctx, repository.NewID(), task.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("cache fallback leaked ownership", err)
	}
}

func TestJWTValidation(t *testing.T) {
	_, auth := testServices(t)
	r := gin.New()
	r.GET("/private", auth.Auth(true), func(c *gin.Context) { c.Status(200) })
	claims := jwt.RegisteredClaims{Issuer: "ai-research-platform", Subject: repository.NewID(), Audience: jwt.ClaimStrings{"ai-research-platform"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}
	for _, tc := range []struct {
		name   string
		change func(*jwt.RegisteredClaims)
		method jwt.SigningMethod
		secret []byte
	}{
		{"expired", func(c *jwt.RegisteredClaims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour)) }, jwt.SigningMethodHS256, []byte(testSecret)},
		{"missing-exp", func(c *jwt.RegisteredClaims) { c.ExpiresAt = nil }, jwt.SigningMethodHS256, []byte(testSecret)},
		{"wrong-issuer", func(c *jwt.RegisteredClaims) { c.Issuer = "other" }, jwt.SigningMethodHS256, []byte(testSecret)},
		{"wrong-algorithm", func(c *jwt.RegisteredClaims) {}, jwt.SigningMethodHS384, []byte(testSecret)},
		{"wrong-secret", func(c *jwt.RegisteredClaims) {}, jwt.SigningMethodHS256, []byte("different-secret")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := claims
			tc.change(&copy)
			token, err := jwt.NewWithClaims(tc.method, copy).SignedString(tc.secret)
			if err != nil {
				t.Fatal(err)
			}
			if w := request(r, "GET", "/private", token, ""); w.Code != 401 {
				t.Fatal("invalid JWT accepted")
			}
		})
	}
}

func TestExistingInputLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/research", newRequestLimits(1, 20).middleware(), handleResearch)
	for _, body := range []string{`{"query":" "}`, `{"query":"x","mode":"bad"}`, `not-json`} {
		if w := request(r, "POST", "/api/research", "", body); w.Code != 400 {
			t.Fatal(w.Code)
		}
	}
	if w := request(r, "POST", "/api/research", "", string(bytes.Repeat([]byte("x"), 65537))); w.Code != 413 {
		t.Fatal(w.Code)
	}
}

func TestRedisIsActuallyAvailable(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("requires test Redis")
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	defer client.Close()
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
}
