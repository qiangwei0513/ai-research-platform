package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/repository"
)

const authExpiryKey = "authenticated_token_expiry"

type EventStream struct {
	Store                                *repository.Store
	PollInterval, Heartbeat, MaxDuration time.Duration
	MaxConnections, MaxPerUser           int
	mu                                   sync.Mutex
	users                                map[string]int
	connections                          int
}

func NewEventStream(store *repository.Store) *EventStream {
	return &EventStream{Store: store, PollInterval: time.Second, Heartbeat: 15 * time.Second, MaxDuration: 2 * time.Minute, MaxConnections: 64, MaxPerUser: 4, users: make(map[string]int)}
}
func (s *EventStream) acquire(user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.connections >= s.MaxConnections || s.users[user] >= s.MaxPerUser {
		return false
	}
	s.connections++
	s.users[user]++
	return true
}
func (s *EventStream) release(user string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.connections--
	s.users[user]--
	if s.users[user] == 0 {
		delete(s.users, user)
	}
}

func (s *EventStream) Serve(c *gin.Context) {
	user, id := UserID(c), c.Param("id")
	expiry, ok := c.Get(authExpiryKey)
	expiresAt, valid := expiry.(time.Time)
	if !ok || !valid || !expiresAt.After(time.Now()) {
		c.JSON(401, gin.H{"error": "登录凭据已过期"})
		return
	}
	check, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
	snapshot, err := s.Store.EventSnapshot(check, user, id)
	cancel()
	if err != nil {
		taskError(c, err)
		return
	}
	cursor := int64(0)
	last := c.GetHeader("Last-Event-ID")
	if last != "" {
		cursor, err = strconv.ParseInt(last, 10, 64)
		if err != nil || cursor < 0 {
			c.JSON(400, gin.H{"error": "Last-Event-ID 必须是非负整数"})
			return
		}
	}
	if cursor > snapshot.LastEventID {
		c.JSON(409, gin.H{"error": "事件游标超出当前记录，请重新获取快照", "code": "event_cursor_ahead"})
		return
	}
	if !s.acquire(user) {
		c.Header("Retry-After", "2")
		c.JSON(429, gin.H{"error": "实时连接过多，请稍后重连"})
		return
	}
	defer s.release(user)
	deadline := time.Now().Add(s.MaxDuration)
	if expiresAt.Before(deadline) {
		deadline = expiresAt
	}
	ctx, stop := context.WithDeadline(c.Request.Context(), deadline)
	defer stop()
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-store, no-transform")
	c.Header("X-Accel-Buffering", "no")
	controller := http.NewResponseController(c.Writer)
	// Do not leave this stream's write deadline on a reused HTTP connection.
	defer controller.SetWriteDeadline(time.Time{})
	write := func(frame string) bool {
		if ctx.Err() != nil {
			return false
		}
		_ = controller.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if _, err := fmt.Fprint(c.Writer, frame); err != nil {
			return false
		}
		return controller.Flush() == nil
	}
	send := func(seq int64, kind string, data any) bool {
		raw, err := json.Marshal(data)
		if err != nil {
			return false
		}
		return write(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", seq, kind, raw))
	}
	streamError := func() {
		write("event: stream_error\ndata: {\"message\":\"事件读取暂时不可用，请重连\"}\n\n")
	}
	read := func(through int64) ([]model.TaskEvent, error) {
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return s.Store.Events(check, user, id, cursor, through)
	}
	if !write("retry: 2000\n\n") {
		return
	}
	// On reconnect replay the missing prefix, then reconcile with one consistent snapshot.
	if last != "" {
		for cursor < snapshot.LastEventID {
			events, err := read(snapshot.LastEventID)
			if err != nil || len(events) == 0 {
				streamError()
				return
			}
			for _, event := range events {
				if !send(event.ID, event.Type, event.Data) {
					return
				}
				cursor = event.ID
			}
		}
	}
	if !send(snapshot.LastEventID, "snapshot", snapshot) {
		return
	}
	cursor = snapshot.LastEventID
	if model.Terminal(snapshot.Task.Status) {
		return
	}
	poll := time.NewTicker(s.PollInterval)
	defer poll.Stop()
	heartbeat := time.NewTicker(s.Heartbeat)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			if !write(": heartbeat\n\n") {
				return
			}
		case <-poll.C:
			events, err := read(math.MaxInt64)
			if err != nil {
				streamError()
				return
			}
			for _, event := range events {
				if !send(event.ID, event.Type, event.Data) {
					return
				}
				cursor = event.ID
				if model.Terminal(event.Data.Status) {
					return
				}
			}
		}
	}
}
