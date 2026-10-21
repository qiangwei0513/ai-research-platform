package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

type OutboxMessage struct{ TaskID, Token string }

// Release the SQL transaction before Redis I/O; publishing leases survive crashes.
func (s *Store) ClaimOutbox(ctx context.Context) (OutboxMessage, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return OutboxMessage{}, false, err
	}
	defer tx.Rollback()
	var m OutboxMessage
	err = tx.QueryRowContext(ctx, "SELECT task_id FROM task_outbox WHERE next_publish_at<=UTC_TIMESTAMP(6) AND (claim_until IS NULL OR claim_until<=UTC_TIMESTAMP(6)) ORDER BY next_publish_at LIMIT 1 FOR UPDATE SKIP LOCKED").Scan(&m.TaskID)
	if errors.Is(err, sql.ErrNoRows) {
		return m, false, nil
	}
	if err != nil {
		return m, false, err
	}
	m.Token = NewID()
	_, err = tx.ExecContext(ctx, "UPDATE task_outbox SET claim_token=?,claim_until=TIMESTAMPADD(SECOND,10,UTC_TIMESTAMP(6)) WHERE task_id=?", m.Token, m.TaskID)
	if err != nil {
		return m, false, err
	}
	return m, true, tx.Commit()
}

// Re-publish while still queued, also repairing Redis data loss. Claims in MySQL
// remove this record; duplicates are safe because execution requires a new lease.
func (s *Store) ReleaseOutbox(ctx context.Context, m OutboxMessage, delay time.Duration) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE task_outbox SET next_publish_at=TIMESTAMPADD(MICROSECOND,?,UTC_TIMESTAMP(6)),claim_token='',claim_until=NULL WHERE task_id=? AND claim_token=?", delay.Microseconds(), m.TaskID, m.Token)
	return err
}
