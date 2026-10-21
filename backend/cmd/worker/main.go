package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/qiangwei0513/ai-research-platform/backend/internal/agent"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/queue"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/service"
)

func setting(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func main() {
	health := flag.Bool("healthcheck", false, "check worker database and queue dependencies")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if os.Getenv("MYSQL_DSN") == "" || os.Getenv("QUEUE_REDIS_URL") == "" {
		log.Fatal("Worker 需要 MYSQL_DSN 和 QUEUE_REDIS_URL")
	}
	budget := 30 * time.Second
	if *health {
		budget = 3 * time.Second
	}
	startup, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	open := repository.OpenWithRetry
	if *health {
		open = repository.Open
	}
	store, err := open(startup, os.Getenv("MYSQL_DSN"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.DB.Close()
	stream, err := queue.New(os.Getenv("QUEUE_REDIS_URL"), setting("QUEUE_PREFIX", "arp"))
	if err != nil {
		log.Fatal(err)
	}
	defer stream.Close()
	if *health {
		if stream.Health(startup) != "ok" {
			log.Fatal("Worker queue unavailable")
		}
		return
	}
	if err = store.Migrate(startup); err != nil {
		log.Fatalf("Worker migration error_type=%T", err)
	}
	cache, err := service.NewTaskCache(os.Getenv("REDIS_URL"), setting("CACHE_PREFIX", "arp"))
	if err != nil {
		log.Fatal(err)
	}
	defer cache.Close()
	count, err := strconv.Atoi(setting("WORKER_CONCURRENCY", "2"))
	if err != nil || count < 1 || count > 16 {
		log.Fatal("WORKER_CONCURRENCY 需在 1～16 范围内")
	}
	tasks := &service.TaskService{Store: store, Cache: cache}
	client := &agent.Client{URL: setting("AGENT_SERVICE_URL", "http://127.0.0.1:8000/research"), OnProgress: tasks.RecordProgress}
	worker := &service.Worker{Tasks: tasks, Queue: stream, Execute: client.Execute, Concurrency: count}
	log.Printf("[worker] starting concurrency=%d", count)
	worker.Run(ctx)
	log.Print("[worker] stopped")
}
