package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
)

var ErrDailyQuota = errors.New("daily task quota reached")

type TaskOptions struct {
	RequestID  string
	DailyLimit int
}

func options(values []TaskOptions) TaskOptions {
	if len(values) > 0 {
		return values[0]
	}
	return TaskOptions{}
}

// Caller holds the user's row lock. The daily boundary is UTC and includes
// failed/cancelled tasks; repeat idempotency keys are checked before this quota.
func checkDaily(ctx context.Context, tx *sql.Tx, user string, limit int) error {
	if limit <= 0 {
		return nil
	}
	var used int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE user_id=? AND started_at>=UTC_DATE()", user).Scan(&used); err != nil {
		return err
	}
	if used >= limit {
		return ErrDailyQuota
	}
	return nil
}
func saveObservation(ctx context.Context, tx *sql.Tx, id, request string) error {
	if len(request) > 64 {
		request = ""
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO task_observations(task_id,request_id) VALUES (?,?)", id, request)
	return err
}
func saveUsage(ctx context.Context, tx *sql.Tx, id string, attempt int, usage *model.Usage) error {
	if !usage.Valid() {
		return errors.New("invalid usage")
	}
	var data any
	if usage != nil {
		raw, err := json.Marshal(usage)
		if err != nil {
			return err
		}
		data = string(raw)
	}
	_, err := tx.ExecContext(ctx, "INSERT INTO task_usage(task_id,attempt,data,recorded_at) VALUES (?,?,?,UTC_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE data=VALUES(data)", id, attempt, data)
	return err
}
func (s *Store) Usage(ctx context.Context, user, id string) ([]map[string]any, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT u.attempt,u.data,COALESCE(o.request_id,'') FROM task_usage u JOIN tasks t ON t.id=u.task_id LEFT JOIN task_observations o ON o.task_id=t.id WHERE t.user_id=? AND t.id=? ORDER BY u.attempt", user, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var attempt int
		var data []byte
		var request string
		if err = rows.Scan(&attempt, &data, &request); err != nil {
			return nil, err
		}
		var usage *model.Usage
		if len(data) > 0 {
			if err = json.Unmarshal(data, &usage); err != nil {
				return nil, err
			}
		}
		items = append(items, map[string]any{"attempt": attempt, "request_id": request, "usage": usage})
	}
	return items, rows.Err()
}
func (s *Store) WorkerHeartbeat(ctx context.Context, id string, concurrency, active int) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO worker_heartbeats VALUES (?,UTC_TIMESTAMP(6),?,?) ON DUPLICATE KEY UPDATE last_seen=UTC_TIMESTAMP(6),concurrency=VALUES(concurrency),active=VALUES(active)", id, concurrency, active)
	if err == nil {
		_, _ = s.DB.ExecContext(ctx, "DELETE FROM worker_heartbeats WHERE last_seen<UTC_TIMESTAMP()-INTERVAL 1 DAY LIMIT 100")
	}
	return err
}
func (s *Store) Metrics(ctx context.Context) (map[string]float64, error) {
	values := map[string]float64{}
	queries := map[string]string{
		"arp_tasks_queued":                  "SELECT COUNT(*) FROM tasks WHERE status='queued'",
		"arp_tasks_running":                 "SELECT COUNT(*) FROM tasks WHERE status='running'",
		"arp_outbox_pending":                "SELECT COUNT(*) FROM task_outbox",
		"arp_workers_alive":                 "SELECT COUNT(*) FROM worker_heartbeats WHERE last_seen>UTC_TIMESTAMP()-INTERVAL 60 SECOND",
		"arp_worker_active":                 "SELECT COALESCE(SUM(active),0) FROM worker_heartbeats WHERE last_seen>UTC_TIMESTAMP()-INTERVAL 60 SECOND",
		"arp_tasks_finished_24h":            "SELECT COUNT(*) FROM tasks WHERE finished_at>=UTC_TIMESTAMP()-INTERVAL 1 DAY",
		"arp_tasks_succeeded_24h":           "SELECT COUNT(*) FROM tasks WHERE finished_at>=UTC_TIMESTAMP()-INTERVAL 1 DAY AND status='succeeded'",
		"arp_tasks_failed_24h":              "SELECT COUNT(*) FROM tasks WHERE finished_at>=UTC_TIMESTAMP()-INTERVAL 1 DAY AND status='failed'",
		"arp_task_duration_seconds_avg_24h": "SELECT COALESCE(AVG(TIMESTAMPDIFF(MICROSECOND,started_at,finished_at))/1000000,0) FROM tasks WHERE finished_at>=UTC_TIMESTAMP()-INTERVAL 1 DAY",
		"arp_usage_unknown_attempts_24h":    "SELECT COUNT(*) FROM task_usage WHERE recorded_at>=UTC_TIMESTAMP()-INTERVAL 1 DAY AND (data IS NULL OR COALESCE(JSON_EXTRACT(data,'$.usage_complete'),false)=false)",
	}
	for _, field := range []string{"input_tokens", "output_tokens", "model_requests", "search_calls", "search_cache_hits", "search_cache_misses", "search_errors"} {
		queries["arp_"+field+"_observed_24h"] = "SELECT COALESCE(SUM(CAST(JSON_UNQUOTE(JSON_EXTRACT(data,'$." + field + "')) AS UNSIGNED)),0) FROM task_usage WHERE recorded_at>=UTC_TIMESTAMP()-INTERVAL 1 DAY"
	}
	for name, query := range queries {
		var value float64
		if err := s.DB.QueryRowContext(ctx, query).Scan(&value); err != nil {
			return nil, err
		}
		values[name] = value
	}
	return values, nil
}
