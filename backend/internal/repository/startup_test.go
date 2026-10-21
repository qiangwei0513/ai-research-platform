package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestStartupRecoversTransientConnectionFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	err := pingWithRetry(ctx, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("connection refused")
		}
		return nil
	}, time.Millisecond)
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestStartupStopsAtDeadlineAndCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		expected := context.DeadlineExceeded
		if cancelled {
			cancel()
			expected = context.Canceled
		}
		err := pingWithRetry(ctx, func(context.Context) error {
			return errors.New("connection refused")
		}, time.Second)
		cancel()
		if !errors.Is(err, expected) {
			t.Fatalf("cancelled=%v err=%v", cancelled, err)
		}
	}
}

func TestStartupDoesNotRetryConfigurationErrors(t *testing.T) {
	for _, code := range []uint16{1044, 1045, 1049} {
		calls := 0
		err := pingWithRetry(context.Background(), func(context.Context) error {
			calls++
			return &mysql.MySQLError{Number: code}
		}, time.Millisecond)
		if err == nil || calls != 1 {
			t.Fatalf("code=%d calls=%d err=%v", code, calls, err)
		}
	}
	if _, err := OpenWithRetry(context.Background(), "unused"); err == nil {
		t.Fatal("unbounded startup must be rejected")
	}
}
