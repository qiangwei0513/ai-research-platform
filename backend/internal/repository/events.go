package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/qiangwei0513/ai-research-platform/backend/internal/model"
)

var ErrInvalidProgress = errors.New("invalid or regressing progress")

// Callers already hold the task row lock, including for lifecycle transitions.
// Counter, event, task state and report commit atomically in the caller's tx.
func appendEventLocked(ctx context.Context, tx *sql.Tx, task model.Task, progress *model.Progress) error {
	if _, err := tx.ExecContext(ctx, "INSERT IGNORE INTO task_event_heads (task_id) VALUES (?)", task.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE task_event_heads SET last_sequence=last_sequence+1 WHERE task_id=?", task.ID); err != nil {
		return err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx, "SELECT last_sequence FROM task_event_heads WHERE task_id=?", task.ID).Scan(&seq); err != nil {
		return err
	}
	kind := task.Status
	if progress != nil {
		kind = progress.EventType()
	}
	data := model.EventData{Status: task.Status, Attempt: task.Attempt, MaxAttempts: task.MaxAttempts, Version: task.Version, Progress: progress, ErrorCode: task.ErrorCode, ErrorMessage: task.ErrorMessage}
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO task_events (task_id,sequence_no,event_type,payload,created_at) VALUES (?,?,?,?,UTC_TIMESTAMP(6))", task.ID, seq, kind, string(raw))
	return err
}

func (s *Store) RecordProgress(ctx context.Context, id, token string, progress model.Progress) (bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	job, err := lockJob(ctx, tx, id)
	if err != nil {
		return false, err
	}
	now, err := databaseNow(ctx, tx)
	if err != nil {
		return false, err
	}
	if job.Task.Status != "running" || job.Token != token || job.LeaseUntil == nil || !job.LeaseUntil.After(now) || !job.Task.DeadlineAt.After(now) {
		return false, ErrConflict
	}
	if !progress.ValidFor(job.Task.Mode) {
		return false, ErrInvalidProgress
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT e.payload FROM task_event_heads h JOIN task_events e ON e.task_id=h.task_id AND e.sequence_no=h.last_sequence WHERE h.task_id=?", id).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if len(raw) > 0 {
		var last model.EventData
		if err = json.Unmarshal(raw, &last); err != nil {
			return false, err
		}
		if last.Attempt == job.Attempt && last.Progress != nil {
			old := last.Progress
			if progress.Rank() < old.Rank() || (progress.Stage == old.Stage && (progress.Total != old.Total || progress.Completed < old.Completed)) {
				return false, ErrInvalidProgress
			}
			if progress == *old {
				return false, nil
			}
		}
	}
	if err = appendEventLocked(ctx, tx, job.View(), &progress); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// A repeatable-read snapshot pairs task state with a committed event watermark.
// Never keep this transaction open for the lifetime of an SSE connection.
func (s *Store) EventSnapshot(ctx context.Context, user, id string) (model.EventSnapshot, error) {
	var snapshot model.EventSnapshot
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return snapshot, err
	}
	defer tx.Rollback()
	snapshot.Task, err = scanTaskView(tx.QueryRowContext(ctx, "SELECT "+taskViewColumns+" FROM tasks t LEFT JOIN task_jobs j ON j.task_id=t.id WHERE t.user_id=? AND t.id=?", user, id))
	if err != nil {
		return snapshot, err
	}
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT h.last_sequence,e.payload FROM task_event_heads h JOIN task_events e ON e.task_id=h.task_id AND e.sequence_no=h.last_sequence WHERE h.task_id=?", id).Scan(&snapshot.LastEventID, &raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return snapshot, err
	}
	if len(raw) > 0 {
		var data model.EventData
		if err = json.Unmarshal(raw, &data); err != nil {
			return snapshot, err
		}
		if data.Attempt == snapshot.Task.Attempt {
			snapshot.Progress = data.Progress
		}
	}
	// Terminal events carry the committed task outcome; preserve the last real
	// stage separately so reconnecting after a failure still shows where it stopped.
	if snapshot.Progress == nil && snapshot.Task.Status != "queued" && snapshot.Task.Attempt > 0 {
		err = tx.QueryRowContext(ctx, "SELECT payload FROM task_events WHERE task_id=? AND event_type IN ('planning','planned','search_progress','writing','answering') ORDER BY sequence_no DESC LIMIT 1", id).Scan(&raw)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return snapshot, err
		}
		if err == nil {
			var data model.EventData
			if err = json.Unmarshal(raw, &data); err != nil {
				return snapshot, err
			}
			if data.Attempt == snapshot.Task.Attempt {
				snapshot.Progress = data.Progress
			}
		}
	}
	return snapshot, tx.Commit()
}
func (s *Store) Events(ctx context.Context, user, id string, after, through int64) ([]model.TaskEvent, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT e.sequence_no,e.event_type,e.payload,e.created_at FROM task_events e JOIN tasks t ON t.id=e.task_id WHERE t.user_id=? AND t.id=? AND e.sequence_no>? AND e.sequence_no<=? ORDER BY e.sequence_no LIMIT 64", user, id, after, through)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]model.TaskEvent, 0)
	for rows.Next() {
		var event model.TaskEvent
		var raw []byte
		if err = rows.Scan(&event.ID, &event.Type, &raw, &event.CreatedAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &event.Data); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
