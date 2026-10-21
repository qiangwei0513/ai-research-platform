package repository

import (
	"context"
	"errors"
	"log"
	"time"

	"github.com/go-sql-driver/mysql"
)

// A bounded backoff handles a database that is still starting (also outside
// Compose). Authentication/unknown database errors require a config fix.
func pingWithRetry(ctx context.Context, ping func(context.Context) error, delay time.Duration) error {
	for attempt := 1; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := ping(ctx)
		if err == nil {
			return nil
		}
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) {
			switch mysqlErr.Number {
			case 1044, 1045, 1049:
				return err
			}
		}
		log.Printf("[startup] mysql not ready attempt=%d retry_in=%s error_type=%T", attempt, delay, err)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		delay = min(delay*2, 2*time.Second)
	}
}
