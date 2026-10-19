package service

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
)

func TestOlderReadCannotReplaceCompletedTask(t *testing.T) {
	url := os.Getenv("TEST_REDIS_URL")
	if url == "" {
		t.Skip("requires test Redis")
	}
	cache, err := NewTaskCache(url, "test-"+repository.NewID())
	if err != nil {
		t.Fatal(err)
	}
	defer cache.Close()
	ctx := context.Background()
	old := model.Task{ID: repository.NewID(), UserID: repository.NewID(), Status: "running", Version: 1}
	completed := old
	completed.Status = "succeeded"
	completed.Version = 2
	cache.put(ctx, completed, time.Now())
	cache.put(ctx, old, time.Now())
	got, ok := cache.get(ctx, old.UserID, old.ID)
	if !ok || got.Status != "succeeded" {
		t.Fatal("late old read overwrote completion", got, ok)
	}
	// 即使终态键已失效，超过原始有效期的旧读取也不得重新写入。
	if err := cache.client.Del(ctx, cache.key(old.UserID, old.ID)).Err(); err != nil {
		t.Fatal(err)
	}
	cache.put(ctx, old, time.Now().Add(-6*time.Second))
	if _, ok := cache.get(ctx, old.UserID, old.ID); ok {
		t.Fatal("expired read repopulated cache")
	}
}
