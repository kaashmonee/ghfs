package github

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ServerError represents an HTTP 5xx error from the GitHub API.
type ServerError struct {
	StatusCode int
	Body       string
}

func (e *ServerError) Error() string {
	return "github: server error " + e.Body
}

// IsServerError checks whether an error message indicates a 5xx server error.
// It inspects both the concrete ServerError type and error messages containing
// "unexpected status 5xx" patterns produced by the client.
func IsServerError(err error) bool {
	var se *ServerError
	if errors.As(err, &se) {
		return true
	}
	msg := err.Error()
	// The client wraps non-2xx responses as "unexpected status NNN".
	// Check for 5xx patterns.
	for _, code := range []string{"500", "502", "503", "504"} {
		if strings.Contains(msg, "unexpected status "+code) {
			return true
		}
	}
	return false
}

// RetryDo executes fn up to maxRetries+1 times with retry logic:
//   - On *RateLimitError: sleeps for RetryAfter duration, then retries
//   - On 5xx server errors: uses exponential backoff (1s, 2s, 4s...)
//   - On other errors: returns immediately without retrying
//   - Respects context cancellation at every sleep boundary
func RetryDo(ctx context.Context, maxRetries int, fn func() error) error {
	var lastErr error
	backoff := time.Second

	for attempt := 0; attempt <= maxRetries; attempt++ {
		lastErr = fn()
		if lastErr == nil {
			return nil
		}

		// Don't retry if we've exhausted attempts.
		if attempt == maxRetries {
			return lastErr
		}

		// Check context before deciding to retry.
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var rateLimitErr *RateLimitError
		if errors.As(lastErr, &rateLimitErr) {
			wait := rateLimitErr.RetryAfter
			if wait <= 0 {
				wait = time.Second
			}
			if err := sleepWithContext(ctx, wait); err != nil {
				return err
			}
			continue
		}

		if IsServerError(lastErr) {
			if err := sleepWithContext(ctx, backoff); err != nil {
				return err
			}
			backoff *= 2
			continue
		}

		// Non-retryable error: return immediately.
		return lastErr
	}

	return lastErr
}

// sleepWithContext sleeps for the given duration or returns early if the
// context is cancelled.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
