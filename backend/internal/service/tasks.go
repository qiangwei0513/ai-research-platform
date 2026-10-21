package service

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"strconv"
	"time"

	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
	"github.com/redis/go-redis/v9"
)

type TaskService struct {
	Store          *repository.Store
	Cache          *TaskCache
	AsyncEnabled   bool
	MaxActiveTasks int
}

func (s *TaskService) RecordProgress(ctx context.Context, job repository.Job, progress model.Progress) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := s.Store.RecordProgress(ctx, job.Task.ID, job.Token, progress)
	return err
}

type TaskCache struct {
	client *redis.Client
	prefix string
}
type cachedTask struct {
	Task       model.Task `json:"task"`
	UserID     string     `json:"user_id"`
	ValidUntil time.Time  `json:"valid_until"`
}

func NewTaskCache(url, prefix string) (*TaskCache, error) {
	if url == "" {
		return nil, nil
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("REDIS_URL 格式无效")
	}
	opts.DialTimeout = 200 * time.Millisecond
	opts.ReadTimeout = 200 * time.Millisecond
	opts.WriteTimeout = 200 * time.Millisecond
	opts.PoolTimeout = 200 * time.Millisecond
	opts.MaxRetries = -1
	opts.ContextTimeoutEnabled = true
	return &TaskCache{client: redis.NewClient(opts), prefix: prefix}, nil
}
func (c *TaskCache) Close() {
	if c != nil {
		_ = c.client.Close()
	}
}
func (c *TaskCache) Health(ctx context.Context) string {
	if c == nil {
		return "disabled"
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if err := c.client.Ping(ctx).Err(); err != nil {
		return "unavailable"
	}
	return "ok"
}
func (c *TaskCache) key(user, id string) string { return c.prefix + ":task:v1:" + user + ":" + id }

func (c *TaskCache) get(ctx context.Context, user, id string) (model.Task, bool) {
	if c == nil {
		return model.Task{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	raw, err := c.client.Get(ctx, c.key(user, id)).Bytes()
	if err != nil {
		if !errors.Is(err, redis.Nil) {
			log.Printf("[cache] task read degraded error_type=%T", err)
		}
		return model.Task{}, false
	}
	var value cachedTask
	if json.Unmarshal(raw, &value) != nil || value.UserID != user || value.Task.ID != id || time.Now().After(value.ValidUntil) {
		return model.Task{}, false
	}
	if value.Task.Status != "queued" && value.Task.Status != "running" && value.Task.Status != "succeeded" && value.Task.Status != "failed" && value.Task.Status != "cancelled" {
		return model.Task{}, false
	}
	value.Task.UserID = user
	return value.Task, true
}

// 原子比较版本：先读到 running 的慢请求不能覆盖已写入的 succeeded/failed。
var setIfNewer = redis.NewScript(`
local old = redis.call('GET', KEYS[1])
if old then
  local ok, value = pcall(cjson.decode, old)
  if ok and value.task and tonumber(value.task.version) and tonumber(value.task.version) > tonumber(ARGV[2]) then return 0 end
end
redis.call('PSETEX', KEYS[1], ARGV[3], ARGV[1])
return 1`)

func (c *TaskCache) put(ctx context.Context, t model.Task, readStarted time.Time) {
	if c == nil {
		return
	}
	ttl := 5 * time.Second
	if t.Status != "running" && t.Status != "queued" {
		ttl = 60 * time.Second
	}
	// 以数据库读取前的时间计算有效期，避免延迟回填延长旧状态的寿命。
	until := readStarted.Add(ttl)
	remaining := time.Until(until).Milliseconds()
	if remaining <= 0 {
		return
	}
	raw, err := json.Marshal(cachedTask{Task: t, UserID: t.UserID, ValidUntil: until})
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
	defer cancel()
	if err := setIfNewer.Run(ctx, c.client, []string{c.key(t.UserID, t.ID)}, string(raw), strconv.FormatInt(t.Version, 10), remaining).Err(); err != nil {
		log.Printf("[cache] task write degraded error_type=%T", err)
	}
}

func (s *TaskService) Get(ctx context.Context, user, id string) (model.Task, error) {
	if t, ok := s.Cache.get(ctx, user, id); ok {
		return t, nil
	}
	started := time.Now()
	t, err := s.Store.Task(ctx, user, id)
	if err == nil {
		s.Cache.put(ctx, t, started)
	}
	return t, err
}
func (s *TaskService) Finish(ctx context.Context, user, id string, report *model.Report, runs []model.AgentRun, code, message string) error {
	t, err := s.Store.Finish(ctx, user, id, report, runs, code, message)
	if err == nil {
		s.Cache.put(ctx, t, time.Now())
	}
	return err
}

// V3 同步请求不恢复模型执行；进程崩溃留下的任务到期后明确标为失败。
func (s *TaskService) RecoverExpired(ctx context.Context) {
	items, err := s.Store.Expired(ctx)
	if err != nil {
		log.Printf("[tasks] expiration scan failed error_type=%T", err)
		return
	}
	for _, t := range items {
		err := s.Finish(ctx, t.UserID, t.ID, nil, nil, "execution_interrupted", "执行超过保存期限或服务中断，请重新提交")
		if err != nil && !errors.Is(err, repository.ErrConflict) {
			log.Printf("[tasks] expiration failed task=%s error_type=%T", t.ID, err)
		}
	}
}

// Cache failure must never roll back a durable task transition.
func (s *TaskService) CacheTask(ctx context.Context, task model.Task) {
	s.Cache.put(ctx, task, time.Now())
}
