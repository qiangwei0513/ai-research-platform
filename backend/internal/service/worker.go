package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/qiangwei0513/ai-research-platform/backend/internal/queue"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
	"github.com/redis/go-redis/v9"
)

type Worker struct {
	Tasks                                                            *TaskService
	Queue                                                            *queue.Streams
	Execute                                                          func(context.Context, repository.Job) repository.Completion
	Concurrency                                                      int
	Lease, Heartbeat, RetryBase, MaintenanceInterval, RepublishAfter time.Duration
	ChatTimeout, ResearchTimeout                                     time.Duration
}

func (w *Worker) defaults() {
	if w.Concurrency < 1 {
		w.Concurrency = 2
	}
	if w.Lease <= 0 {
		w.Lease = 30 * time.Second
	}
	if w.Heartbeat <= 0 {
		w.Heartbeat = 2 * time.Second
	}
	if w.RetryBase <= 0 {
		w.RetryBase = 5 * time.Second
	}
	if w.MaintenanceInterval <= 0 {
		w.MaintenanceInterval = time.Second
	}
	if w.RepublishAfter <= 0 {
		w.RepublishAfter = 30 * time.Second
	}
	if w.ChatTimeout <= 0 {
		w.ChatTimeout = 130 * time.Second
	}
	if w.ResearchTimeout <= 0 {
		w.ResearchTimeout = 310 * time.Second
	}
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
func (w *Worker) Run(ctx context.Context) {
	w.defaults()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for ctx.Err() == nil {
			w.maintain(ctx)
			if !pause(ctx, w.MaintenanceInterval) {
				return
			}
		}
	}()
	instance := repository.NewID()
	for i := 0; i < w.Concurrency; i++ {
		wg.Add(1)
		go func(slot int) { defer wg.Done(); w.consume(ctx, fmt.Sprintf("%s-%d", instance, slot)) }(i)
	}
	wg.Wait()
}
func (w *Worker) maintain(ctx context.Context) {
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	ids, err := w.Tasks.Store.RecoverableJobs(check)
	cancel()
	if err != nil {
		log.Printf("[worker] recovery scan error_type=%T", err)
	}
	for _, id := range ids {
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		task, changed, err := w.Tasks.Store.RecoverJob(check, id, w.RetryBase)
		if err == nil && changed {
			w.Tasks.Cache.put(check, task, time.Now())
			log.Printf("[worker] recovered task=%s status=%s", id, task.Status)
		}
		cancel()
		if err != nil {
			log.Printf("[worker] recovery task=%s error_type=%T", id, err)
		}
	}
	for i := 0; i < 20 && ctx.Err() == nil; i++ {
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		message, found, err := w.Tasks.Store.ClaimOutbox(check)
		cancel()
		if err != nil {
			log.Printf("[outbox] claim error_type=%T", err)
			break
		}
		if !found {
			break
		}
		send, cancel := context.WithTimeout(ctx, 2*time.Second)
		err = w.Queue.Publish(send, message.TaskID)
		cancel()
		delay := w.RepublishAfter
		if err != nil {
			delay = 2 * time.Second
			log.Printf("[outbox] publish task=%s error_type=%T", message.TaskID, err)
		}
		saved, cancel := context.WithTimeout(ctx, 5*time.Second)
		if saveErr := w.Tasks.Store.ReleaseOutbox(saved, message, delay); saveErr != nil {
			log.Printf("[outbox] release task=%s error_type=%T", message.TaskID, saveErr)
		}
		cancel()
		if err != nil {
			break
		}
	}
}
func (w *Worker) consume(ctx context.Context, consumer string) {
	ready := false
	cursor := "0-0"
	for ctx.Err() == nil {
		if !ready {
			if err := w.Queue.Ensure(ctx); err != nil {
				if !pause(ctx, time.Second) {
					return
				}
				continue
			}
			ready = true
		}
		messages, next, err := w.Queue.Reclaim(ctx, consumer, cursor, w.Lease)
		if err == nil {
			cursor = next
			for _, message := range messages {
				w.process(ctx, message)
			}
		}
		if err == nil {
			var m redis.XMessage
			m, err = w.Queue.Read(ctx, consumer)
			if err == nil {
				w.process(ctx, m)
			}
		}
		if err != nil && !errors.Is(err, redis.Nil) && ctx.Err() == nil {
			if strings.Contains(err.Error(), "NOGROUP") {
				ready = false
			}
			log.Printf("[worker] queue read error_type=%T", err)
			if !pause(ctx, time.Second) {
				return
			}
		}
	}
}
func (w *Worker) ack(ctx context.Context, id string) {
	check, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := w.Queue.Ack(check, id); err != nil {
		log.Printf("[worker] ack message=%s error_type=%T", id, err)
	}
}
func (w *Worker) process(ctx context.Context, message redis.XMessage) {
	id, ok := message.Values["task_id"].(string)
	if !ok || len(id) != 32 {
		w.ack(ctx, message.ID)
		return
	}
	check, cancel := context.WithTimeout(ctx, 5*time.Second)
	job, claimed, err := w.Tasks.Store.ClaimJob(check, id, w.Lease)
	if err == nil && claimed {
		w.Tasks.Cache.put(check, job.View(), time.Now())
	}
	cancel()
	if err != nil {
		log.Printf("[worker] claim task=%s error_type=%T", id, err)
		return
	}
	// Even a duplicate delivery is safe to acknowledge: the durable job lease and
	// recovery/outbox records, not a Redis message alone, determine pending work.
	if !claimed {
		w.ack(ctx, message.ID)
		return
	}
	budget := w.ChatTimeout
	if job.Task.Mode == "research" {
		budget = w.ResearchTimeout
	}
	deadline := time.Now().Add(budget)
	if job.Task.DeadlineAt.Before(deadline) {
		deadline = job.Task.DeadlineAt
	}
	runCtx, stop := context.WithDeadline(ctx, deadline)
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(w.Heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
				check, cancel := context.WithTimeout(runCtx, 3*time.Second)
				owned, err := w.Tasks.Store.RenewJob(check, id, job.Token, w.Lease)
				cancel()
				if err != nil || !owned {
					stop()
					return
				}
			}
		}
	}()
	log.Printf("[worker] start task=%s attempt=%d mode=%s", id, job.Attempt, job.Task.Mode)
	result := w.Execute(runCtx, job)
	runError := runCtx.Err()
	stop()
	<-heartbeatDone
	if ctx.Err() != nil {
		result = repository.Completion{Code: "worker_stopped", Message: "执行进程停止，等待重新执行", Retryable: true}
	} else if errors.Is(runError, context.DeadlineExceeded) {
		result = repository.Completion{Code: "upstream_timeout", Message: "研究服务等待超时", Retryable: true}
	}
	finish, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	task, err := w.Tasks.Store.CompleteJob(finish, id, job.Token, result, w.RetryBase)
	if err == nil {
		w.Tasks.Cache.put(finish, task, time.Now())
		log.Printf("[worker] finish task=%s attempt=%d status=%s code=%s", id, job.Attempt, task.Status, task.ErrorCode)
	}
	cancel()
	if err != nil && !errors.Is(err, repository.ErrConflict) {
		log.Printf("[worker] finish task=%s error_type=%T", id, err)
		return
	}
	ack, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w.ack(ack, message.ID)
}
