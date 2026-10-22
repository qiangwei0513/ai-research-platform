package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
)

var ErrTooManyTasks = errors.New("too many active tasks")

const taskViewColumns = "t.id,t.user_id,t.query,t.mode,t.status,t.started_at,t.finished_at,t.deadline_at,t.error_code,t.error_message,t.version,(j.task_id IS NOT NULL),COALESCE(j.attempt,0),COALESCE(j.max_attempts,0),j.available_at,j.attempt_started_at"

func scanTaskView(row scanner) (model.Task, error) {
	var t model.Task
	err := row.Scan(&t.ID, &t.UserID, &t.Query, &t.Mode, &t.Status, &t.StartedAt, &t.FinishedAt, &t.DeadlineAt, &t.ErrorCode, &t.ErrorMessage, &t.Version, &t.Async, &t.Attempt, &t.MaxAttempts, &t.NextAttemptAt, &t.ExecutionStartedAt)
	if t.Status != "queued" {
		t.NextAttemptAt = nil
	}
	return t, err
}

type Job struct {
	RequestID   string
	Task        model.Task
	Fresh       bool
	Attempt     int
	MaxAttempts int
	AvailableAt time.Time
	StartedAt   *time.Time
	Token       string
	LeaseUntil  *time.Time
}

func (j Job) View() model.Task {
	t := j.Task
	t.Async, t.Attempt, t.MaxAttempts = true, j.Attempt, j.MaxAttempts
	t.ExecutionStartedAt = j.StartedAt
	if t.Status == "queued" {
		t.NextAttemptAt = &j.AvailableAt
	}
	return t
}
func databaseNow(ctx context.Context, tx *sql.Tx) (time.Time, error) {
	var now time.Time
	err := tx.QueryRowContext(ctx, "SELECT UTC_TIMESTAMP(6)").Scan(&now)
	return now, err
}

// Every transition locks the task before its job. Lease tokens fence old workers.
func lockJob(ctx context.Context, tx *sql.Tx, id string) (Job, error) {
	var j Job
	var err error
	j.Task, err = scanTask(tx.QueryRowContext(ctx, "SELECT "+taskColumns+" FROM tasks WHERE id=? FOR UPDATE", id))
	if err != nil {
		return j, err
	}
	err = tx.QueryRowContext(ctx, "SELECT fresh,attempt,max_attempts,available_at,attempt_started_at,lease_token,lease_until FROM task_jobs WHERE task_id=? FOR UPDATE", id).Scan(&j.Fresh, &j.Attempt, &j.MaxAttempts, &j.AvailableAt, &j.StartedAt, &j.Token, &j.LeaseUntil)
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT COALESCE((SELECT request_id FROM task_observations WHERE task_id=?),'')", id).Scan(&j.RequestID)
	}
	return j, err
}

// User locking serializes per-user quotas and idempotency. Task, job and outbox
// commit together, so accepting a request does not require Redis availability.
func (s *Store) CreateAsyncTask(ctx context.Context, user, query, mode string, fresh bool, key string, maxActive int, opts ...TaskOptions) (model.Task, bool, error) {
	payload, _ := json.Marshal(struct {
		Query, Mode string
		Fresh       bool
	}{query, mode, fresh})
	sum := sha256.Sum256(payload)
	fingerprint := hex.EncodeToString(sum[:])
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, false, err
	}
	defer tx.Rollback()
	var userID string
	if err = tx.QueryRowContext(ctx, "SELECT id FROM users WHERE id=? FOR UPDATE", user).Scan(&userID); err != nil {
		return model.Task{}, false, err
	}
	var existing, hash string
	err = tx.QueryRowContext(ctx, "SELECT task_id,request_hash FROM task_jobs WHERE user_id=? AND request_key=?", user, key).Scan(&existing, &hash)
	if err == nil {
		if hash != fingerprint {
			return model.Task{}, false, ErrConflict
		}
		t, err := scanTaskView(tx.QueryRowContext(ctx, "SELECT "+taskViewColumns+" FROM tasks t JOIN task_jobs j ON j.task_id=t.id WHERE t.id=?", existing))
		return t, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return model.Task{}, false, err
	}
	if err = checkDaily(ctx, tx, user, options(opts).DailyLimit); err != nil {
		return model.Task{}, false, err
	}
	var active int
	if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM tasks WHERE user_id=? AND status IN ('queued','running')", user).Scan(&active); err != nil {
		return model.Task{}, false, err
	}
	if active >= maxActive {
		return model.Task{}, false, ErrTooManyTasks
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return model.Task{}, false, err
	}
	t := model.Task{ID: NewID(), UserID: user, Query: query, Mode: mode, Status: "queued", StartedAt: now, DeadlineAt: now.Add(30 * time.Minute), Version: 1, Async: true, MaxAttempts: 3, NextAttemptAt: &now}
	_, err = tx.ExecContext(ctx, "INSERT INTO tasks (id,user_id,query,mode,status,started_at,deadline_at,error_message) VALUES (?,?,?,?,?,?,?,'')", t.ID, user, query, mode, t.Status, now, t.DeadlineAt)
	if err != nil {
		return t, false, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO task_jobs (task_id,user_id,request_key,request_hash,fresh,available_at) VALUES (?,?,?,?,?,?)", t.ID, user, key, fingerprint, fresh, now)
	if err != nil {
		return t, false, err
	}
	if err = enqueueOutbox(ctx, tx, t.ID, now); err != nil {
		return t, false, err
	}
	if err = appendEventLocked(ctx, tx, t, nil); err != nil {
		return t, false, err
	}
	if err = saveObservation(ctx, tx, t.ID, options(opts).RequestID); err != nil {
		return t, false, err
	}
	return t, true, tx.Commit()
}
func enqueueOutbox(ctx context.Context, tx *sql.Tx, id string, available time.Time) error {
	_, err := tx.ExecContext(ctx, "INSERT INTO task_outbox (task_id,next_publish_at) VALUES (?,?) ON DUPLICATE KEY UPDATE next_publish_at=?,claim_token='',claim_until=NULL", id, available, available)
	return err
}

func (s *Store) ClaimJob(ctx context.Context, id string, lease time.Duration) (Job, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, err
	}
	defer tx.Rollback()
	j, err := lockJob(ctx, tx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return j, false, nil
	}
	if err != nil {
		return j, false, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return j, false, err
	}
	if j.Task.Status != "queued" || j.AvailableAt.After(now) || !j.Task.DeadlineAt.After(now) || j.Attempt >= j.MaxAttempts {
		return j, false, nil
	}
	j.Attempt++
	j.Token = NewID()
	j.StartedAt = &now
	until := now.Add(lease)
	j.LeaseUntil = &until
	j.Task.Status = "running"
	j.Task.Version++
	j.Task.ErrorCode = ""
	j.Task.ErrorMessage = ""
	_, err = tx.ExecContext(ctx, "UPDATE tasks SET status='running',error_code='',error_message='',version=version+1 WHERE id=?", id)
	if err != nil {
		return j, false, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE task_jobs SET attempt=?,attempt_started_at=?,lease_token=?,lease_until=? WHERE task_id=?", j.Attempt, now, j.Token, until, id)
	if err != nil {
		return j, false, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO task_attempts (task_id,attempt,status,started_at,error_message) VALUES (?,?,'running',?,'')", id, j.Attempt, now)
	if err != nil {
		return j, false, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM task_outbox WHERE task_id=?", id); err != nil {
		return j, false, err
	}
	if err = appendEventLocked(ctx, tx, j.View(), nil); err != nil {
		return j, false, err
	}
	return j, true, tx.Commit()
}

// Renewal cannot resurrect an expired lease, a cancelled task or an older attempt.
func (s *Store) RenewJob(ctx context.Context, id, token string, lease time.Duration) (bool, error) {
	result, err := s.DB.ExecContext(ctx, "UPDATE task_jobs j JOIN tasks t ON t.id=j.task_id SET j.lease_until=TIMESTAMPADD(MICROSECOND,?,UTC_TIMESTAMP(6)) WHERE j.task_id=? AND j.lease_token=? AND j.lease_until>UTC_TIMESTAMP(6) AND t.status='running' AND t.deadline_at>UTC_TIMESTAMP(6)", lease.Microseconds(), id, token)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	return n == 1, err
}

type Completion struct {
	Usage     *model.Usage
	Report    *model.Report
	Runs      []model.AgentRun
	Code      string
	Message   string
	Retryable bool
}

func (s *Store) CompleteJob(ctx context.Context, id, token string, result Completion, retryBase time.Duration) (model.Task, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback()
	j, err := lockJob(ctx, tx, id)
	if err != nil {
		return j.Task, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return j.Task, err
	}
	if j.Task.Status != "running" || j.Token != token || j.LeaseUntil == nil || !j.LeaseUntil.After(now) || !j.Task.DeadlineAt.After(now) {
		return j.View(), ErrConflict
	}
	t, err := completeLocked(ctx, tx, j, result, retryBase, now)
	if err != nil {
		return t, err
	}
	return t, tx.Commit()
}
func completeLocked(ctx context.Context, tx *sql.Tx, j Job, result Completion, retryBase time.Duration, now time.Time) (model.Task, error) {
	t := j.Task
	if j.Attempt > 0 && j.Task.Status == "running" {
		if err := saveUsage(ctx, tx, t.ID, j.Attempt, result.Usage); err != nil {
			return t, err
		}
	}
	status := "failed"
	if result.Report != nil {
		status = "succeeded"
	}
	if j.Attempt > 0 && j.Task.Status == "running" {
		runs := result.Runs
		if runs == nil {
			runs = []model.AgentRun{}
		}
		raw, err := json.Marshal(runs)
		if err != nil {
			return t, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE task_attempts SET status=?,finished_at=?,error_code=?,error_message=?,runs=? WHERE task_id=? AND attempt=?", status, now, result.Code, result.Message, string(raw), t.ID, j.Attempt)
		if err != nil {
			return t, err
		}
	}
	delay := retryBase * time.Duration(1<<max(0, min(j.Attempt-1, 5)))
	available := now.Add(delay)
	if result.Report == nil && result.Retryable && j.Attempt < j.MaxAttempts && available.Before(t.DeadlineAt) {
		t.Status = "queued"
		t.Version++
		t.ErrorCode = result.Code
		t.ErrorMessage = result.Message
		_, err := tx.ExecContext(ctx, "UPDATE tasks SET status='queued',version=version+1,error_code=?,error_message=? WHERE id=?", result.Code, result.Message, t.ID)
		if err != nil {
			return t, err
		}
		if err = enqueueOutbox(ctx, tx, t.ID, available); err != nil {
			return t, err
		}
	} else {
		var err error
		t, err = saveTaskResult(ctx, tx, t, result.Report, result.Runs, result.Code, result.Message)
		if err != nil {
			return t, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM task_outbox WHERE task_id=?", t.ID); err != nil {
			return t, err
		}
	}
	_, err := tx.ExecContext(ctx, "UPDATE task_jobs SET available_at=?,lease_token='',lease_until=NULL WHERE task_id=?", available, t.ID)
	j.Task, j.AvailableAt = t, available
	if err == nil {
		err = appendEventLocked(ctx, tx, j.View(), nil)
	}
	return j.View(), err
}

func (s *Store) CancelJob(ctx context.Context, user, id string) (model.Task, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, err
	}
	defer tx.Rollback()
	j, err := lockJob(ctx, tx, id)
	if err != nil {
		return model.Task{}, err
	}
	if j.Task.UserID != user {
		return model.Task{}, sql.ErrNoRows
	}
	if j.Task.Status == "cancelled" {
		return j.View(), nil
	}
	if j.Task.Status != "queued" && j.Task.Status != "running" {
		return j.View(), ErrConflict
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return j.Task, err
	}
	if j.Task.Status == "running" {
		_, err = tx.ExecContext(ctx, "UPDATE task_attempts SET status='cancelled',finished_at=?,error_code='cancelled',error_message='用户取消任务' WHERE task_id=? AND attempt=?", now, id, j.Attempt)
		if err != nil {
			return j.Task, err
		}
	}
	_, err = tx.ExecContext(ctx, "UPDATE tasks SET status='cancelled',finished_at=?,error_code='cancelled',error_message='用户取消任务',version=version+1 WHERE id=?", now, id)
	if err != nil {
		return j.Task, err
	}
	if _, err = tx.ExecContext(ctx, "UPDATE task_jobs SET lease_token='',lease_until=NULL WHERE task_id=?", id); err != nil {
		return j.Task, err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM task_outbox WHERE task_id=?", id); err != nil {
		return j.Task, err
	}
	j.Task.Status = "cancelled"
	j.Task.FinishedAt = &now
	j.Task.ErrorCode = "cancelled"
	j.Task.ErrorMessage = "用户取消任务"
	j.Task.Version++
	if err = appendEventLocked(ctx, tx, j.View(), nil); err != nil {
		return j.View(), err
	}
	return j.View(), tx.Commit()
}
func (s *Store) RecoverableJobs(ctx context.Context) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT t.id FROM tasks t JOIN task_jobs j ON j.task_id=t.id WHERE (t.status='running' AND j.lease_until<=UTC_TIMESTAMP(6)) OR (t.status IN ('queued','running') AND t.deadline_at<=UTC_TIMESTAMP(6)) LIMIT 100")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
func (s *Store) RecoverJob(ctx context.Context, id string, retryBase time.Duration) (model.Task, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return model.Task{}, false, err
	}
	defer tx.Rollback()
	j, err := lockJob(ctx, tx, id)
	if err != nil {
		return j.Task, false, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return j.Task, false, err
	}
	result := Completion{Code: "worker_lost", Message: "执行进程中断，等待重新执行", Retryable: true}
	if j.Task.Status != "queued" && j.Task.Status != "running" {
		return j.View(), false, nil
	}
	if !j.Task.DeadlineAt.After(now) {
		result = Completion{Code: "task_expired", Message: "任务超过 30 分钟有效期，请重新提交"}
	} else if j.Task.Status != "running" || j.LeaseUntil == nil || j.LeaseUntil.After(now) {
		return j.View(), false, nil
	}
	t, err := completeLocked(ctx, tx, j, result, retryBase, now)
	if err != nil {
		return t, false, err
	}
	return t, true, tx.Commit()
}
func (s *Store) Attempts(ctx context.Context, user, id string) ([]model.Attempt, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT a.attempt,a.status,a.started_at,a.finished_at,a.error_code,a.error_message,a.runs FROM task_attempts a JOIN tasks t ON t.id=a.task_id WHERE t.user_id=? AND t.id=? ORDER BY a.attempt", user, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]model.Attempt, 0)
	for rows.Next() {
		var a model.Attempt
		var raw []byte
		if err = rows.Scan(&a.Attempt, &a.Status, &a.StartedAt, &a.FinishedAt, &a.ErrorCode, &a.ErrorMessage, &raw); err != nil {
			return nil, err
		}
		a.Runs = []model.AgentRun{}
		if len(raw) > 0 {
			if err = json.Unmarshal(raw, &a.Runs); err != nil {
				return nil, err
			}
		}
		items = append(items, a)
	}
	return items, rows.Err()
}
