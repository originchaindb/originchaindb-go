package originchaindb

import (
	"context"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const defaultMaxRetryTime = 30 * time.Second

// retryableStatus reports the statuses a read may be sent again after.
func retryableStatus(status int) bool {
	switch status {
	case http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryBackoff is a full-jitter delay: up to 250 ms doubling per attempt,
// capped at four seconds. Tests replace it.
var retryBackoff = func(attempt int) time.Duration {
	limit := min(4*time.Second, 250*time.Millisecond<<min(attempt, 4))
	return rand.N(limit)
}

// retrySleep waits for d or until ctx ends. Tests replace it.
var retrySleep = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// retryPause waits before another attempt. It reports false, without waiting,
// when the wait would end after deadline. A context that ends during the wait
// returns that context's error.
func retryPause(ctx context.Context, deadline time.Time, wait time.Duration) (bool, error) {
	if wait >= time.Until(deadline) {
		return false, nil
	}
	if err := retrySleep(ctx, wait); err != nil {
		return false, contextError(err)
	}
	return true, nil
}

// parseRetryAfter reads delay-seconds or an HTTP-date. An explicit zero is
// kept. A very large delay saturates instead of overflowing.
func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if strings.Trim(value, "0123456789") == "" {
		seconds, err := strconv.ParseInt(value, 10, 64)
		if err != nil || seconds > int64(time.Duration(1<<62)/time.Second) {
			return time.Duration(1 << 62), true
		}
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(0, date.Sub(now)), true
}
