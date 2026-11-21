package hansho

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	defaultMaxRetries = 3
	baseBackoff       = 250 * time.Millisecond
	maxBackoff        = 8 * time.Second
	// maxRetryWait bounds how long the client waits on a Retry-After; a longer
	// request is surfaced to the caller instead.
	maxRetryWait = 60 * time.Second
)

// idempotent reports whether a method may be repeated safely.
func idempotent(method string) bool {
	return method == http.MethodGet || method == http.MethodPut || method == http.MethodDelete
}

func retryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

// backoff is exponential with full jitter in [d/2, d].
func backoff(attempt int) time.Duration {
	d := baseBackoff << min(attempt, 10)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	return d/2 + time.Duration(rand.Int64N(int64(d/2)+1))
}

// parseRetryAfter reads delay-seconds or an HTTP date.
func parseRetryAfter(v string, now time.Time) time.Duration {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0
	}
	if n, err := strconv.ParseFloat(v, 64); err == nil {
		if n < 0 {
			return 0
		}
		return time.Duration(n * float64(time.Second))
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
