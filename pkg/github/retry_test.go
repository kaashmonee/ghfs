package github

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRetryDo_RetriesOnRateLimitError(t *testing.T) {
	attempts := 0
	err := RetryDo(context.Background(), 3, func() error {
		attempts++
		if attempts == 1 {
			return &RateLimitError{RetryAfter: 10 * time.Millisecond}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestRetryDo_GivesUpAfterMaxRetries(t *testing.T) {
	attempts := 0
	err := RetryDo(context.Background(), 2, func() error {
		attempts++
		return &RateLimitError{RetryAfter: 10 * time.Millisecond}
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	var rle *RateLimitError
	if !errors.As(err, &rle) {
		t.Fatalf("expected RateLimitError, got %T: %v", err, err)
	}
	// 1 initial + 2 retries = 3 total attempts
	if attempts != 3 {
		t.Fatalf("expected 3 attempts, got %d", attempts)
	}
}

func TestRetryDo_DoesNotRetryOnNonRetryableError(t *testing.T) {
	attempts := 0
	sentinel := errors.New("bad request: 400")
	err := RetryDo(context.Background(), 3, func() error {
		attempts++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected sentinel error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt (no retries), got %d", attempts)
	}
}

func TestRetryDo_RespectsContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	attempts := 0
	err := RetryDo(ctx, 5, func() error {
		attempts++
		// Cancel the context after the first attempt so the sleep is interrupted.
		cancel()
		return &RateLimitError{RetryAfter: 10 * time.Second}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}

func TestRetryDo_RetriesOnServerError(t *testing.T) {
	attempts := 0
	err := RetryDo(context.Background(), 3, func() error {
		attempts++
		if attempts == 1 {
			return &ServerError{StatusCode: 500, Body: "internal server error"}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestRetryDo_RetriesOnUnexpectedStatus500(t *testing.T) {
	attempts := 0
	err := RetryDo(context.Background(), 3, func() error {
		attempts++
		if attempts == 1 {
			return errors.New("github: PutFile unexpected status 500: server error")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}

func TestRetryDo_SucceedsOnFirstAttempt(t *testing.T) {
	attempts := 0
	err := RetryDo(context.Background(), 3, func() error {
		attempts++
		return nil
	})
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if attempts != 1 {
		t.Fatalf("expected 1 attempt, got %d", attempts)
	}
}
