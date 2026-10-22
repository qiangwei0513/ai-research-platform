// Package observe keeps correlation IDs out of metric labels and never logs bodies.
package observe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type idsKey struct{}
type IDs struct {
	Request, Task string
	Attempt       int
}

var safeID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func Configure(component string) {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", component))
}
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func RequestID(value string) string {
	if safeID.MatchString(value) {
		return value
	}
	return NewID()
}
func WithIDs(ctx context.Context, request, task string, attempt int) context.Context {
	return context.WithValue(ctx, idsKey{}, IDs{Request: request, Task: task, Attempt: attempt})
}
func From(ctx context.Context) IDs { id, _ := ctx.Value(idsKey{}).(IDs); return id }
func Log(ctx context.Context, event string, fields ...any) {
	id := From(ctx)
	args := []any{"request_id", id.Request, "task_id", id.Task, "attempt", id.Attempt}
	slog.Info(event, append(args, fields...)...)
}

var counters = struct {
	sync.Mutex
	values map[string]float64
}{values: make(map[string]float64)}

// All callers supply fixed metric/label names and bounded label values.
func Add(name string, value float64, labels ...string) {
	key := name
	if len(labels) > 0 {
		parts := make([]string, 0, len(labels)/2)
		for i := 0; i+1 < len(labels); i += 2 {
			parts = append(parts, fmt.Sprintf("%s=%q", labels[i], labels[i+1]))
		}
		key += "{" + strings.Join(parts, ",") + "}"
	}
	counters.Lock()
	counters.values[key] += value
	counters.Unlock()
}
func HTTP(route, method string, status int, elapsed time.Duration) {
	switch method {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
	default:
		method = "OTHER"
	}
	labels := []string{"route", route, "method", method}
	Add("arp_http_requests_total", 1, append(labels, "status", fmt.Sprint(status))...)
	Add("arp_http_duration_seconds_sum", elapsed.Seconds(), labels...)
	Add("arp_http_duration_seconds_count", 1, labels...)
	for _, bound := range []float64{.01, .05, .1, .5, 1, 5, 30, 120, 330} {
		count := float64(0)
		if elapsed.Seconds() <= bound {
			count = 1
		}
		Add("arp_http_duration_seconds_bucket", count, append(labels, "le", fmt.Sprint(bound))...)
	}
	Add("arp_http_duration_seconds_bucket", 1, append(labels, "le", "+Inf")...)
}
func Snapshot() string {
	counters.Lock()
	defer counters.Unlock()
	keys := make([]string, 0, len(counters.values))
	for k := range counters.values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&out, "%s %g\n", key, counters.values[key])
	}
	return out.String()
}
