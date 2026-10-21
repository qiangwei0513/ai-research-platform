package queue

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Streams struct {
	Client     *redis.Client
	Key, Group string
}

func New(url, prefix string) (*Streams, error) {
	if strings.TrimSpace(url) == "" {
		return nil, nil
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, errors.New("QUEUE_REDIS_URL 格式无效")
	}
	opts.DialTimeout = time.Second
	opts.ReadTimeout = 2 * time.Second
	opts.WriteTimeout = time.Second
	opts.PoolTimeout = time.Second
	opts.MaxRetries = -1
	opts.ContextTimeoutEnabled = true
	return &Streams{Client: redis.NewClient(opts), Key: prefix + ":jobs:v1", Group: "workers"}, nil
}
func (q *Streams) Close() {
	if q != nil {
		_ = q.Client.Close()
	}
}
func (q *Streams) Health(ctx context.Context) string {
	if q == nil {
		return "disabled"
	}
	check, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	if q.Client.Ping(check).Err() != nil {
		return "unavailable"
	}
	return "ok"
}
func (q *Streams) Ensure(ctx context.Context) error {
	err := q.Client.XGroupCreateMkStream(ctx, q.Key, q.Group, "0").Err()
	if err != nil && strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}
func (q *Streams) Publish(ctx context.Context, id string) error {
	// Only the opaque task ID crosses the queue; query, user and credentials stay in SQL.
	return q.Client.XAdd(ctx, &redis.XAddArgs{Stream: q.Key, Values: map[string]any{"task_id": id}}).Err()
}
func (q *Streams) Read(ctx context.Context, consumer string) (redis.XMessage, error) {
	rows, err := q.Client.XReadGroup(ctx, &redis.XReadGroupArgs{Group: q.Group, Consumer: consumer, Streams: []string{q.Key, ">"}, Count: 1, Block: time.Second}).Result()
	if err != nil {
		return redis.XMessage{}, err
	}
	if len(rows) == 0 || len(rows[0].Messages) == 0 {
		return redis.XMessage{}, redis.Nil
	}
	return rows[0].Messages[0], nil
}
func (q *Streams) Reclaim(ctx context.Context, consumer, cursor string, idle time.Duration) ([]redis.XMessage, string, error) {
	return q.Client.XAutoClaim(ctx, &redis.XAutoClaimArgs{Stream: q.Key, Group: q.Group, Consumer: consumer, MinIdle: idle, Start: cursor, Count: 1}).Result()
}

var ackDelete = redis.NewScript(`redis.call('XACK',KEYS[1],ARGV[1],ARGV[2]); return redis.call('XDEL',KEYS[1],ARGV[2])`)

func (q *Streams) Ack(ctx context.Context, id string) error {
	// This stream has one consumer group. Do not trim unacknowledged work by MAXLEN.
	return ackDelete.Run(ctx, q.Client, []string{q.Key}, q.Group, id).Err()
}
