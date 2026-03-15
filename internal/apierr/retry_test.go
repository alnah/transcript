package apierr_test

// Coverage Notes:
// - Tests relocated from transcriber_test.go to apierr package where RetryWithBackoff now lives.
// - Tests verify retry count, shouldRetry filtering, context cancellation, and config normalization.
// - Exact backoff timing is not tested (implementation detail), only observable behavior.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alnah/transcript/internal/apierr"
)

// ---------------------------------------------------------------------------
// TestRetryWithBackoff - Generic retry utility
// ---------------------------------------------------------------------------

func TestRetryWithBackoff(t *testing.T) {
	t.Parallel()

	t.Run("success on first try returns immediately", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		result, err := apierr.RetryWithBackoff(
			context.Background(),
			apierr.RetryConfig{MaxRetries: 5, BaseDelay: time.Second, MaxDelay: time.Minute},
			func() (string, error) {
				callCount++
				return "immediate", nil
			},
			func(error) bool { return true },
		)

		if err != nil {
			t.Errorf("RetryWithBackoff() unexpected error: %v", err)
		}
		if result != "immediate" {
			t.Errorf("got %q, want %q", result, "immediate")
		}
		if callCount != 1 {
			t.Errorf("call count = %d, want 1", callCount)
		}
	})

	t.Run("shouldRetry false stops immediately", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		testErr := errors.New("non-retryable")
		_, err := apierr.RetryWithBackoff(
			context.Background(),
			apierr.RetryConfig{MaxRetries: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
			func() (string, error) {
				callCount++
				return "", testErr
			},
			func(error) bool { return false },
		)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if callCount != 1 {
			t.Errorf("call count = %d, want 1 (no retry)", callCount)
		}
	})

	t.Run("MaxRetries 0 means single attempt", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		testErr := errors.New("always fails")
		_, err := apierr.RetryWithBackoff(
			context.Background(),
			apierr.RetryConfig{MaxRetries: 0, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
			func() (string, error) {
				callCount++
				return "", testErr
			},
			func(error) bool { return true },
		)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if callCount != 1 {
			t.Errorf("call count = %d, want 1", callCount)
		}
	})

	t.Run("retries then succeeds", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		result, err := apierr.RetryWithBackoff(
			context.Background(),
			apierr.RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
			func() (string, error) {
				callCount++
				if callCount < 3 {
					return "", errors.New("transient")
				}
				return "success", nil
			},
			func(error) bool { return true },
		)

		if err != nil {
			t.Errorf("RetryWithBackoff() unexpected error: %v", err)
		}
		if result != "success" {
			t.Errorf("got %q, want %q", result, "success")
		}
		if callCount != 3 {
			t.Errorf("call count = %d, want 3", callCount)
		}
	})

	t.Run("max retries exceeded wraps last error", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		testErr := errors.New("always fails")
		_, err := apierr.RetryWithBackoff(
			context.Background(),
			apierr.RetryConfig{MaxRetries: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
			func() (string, error) {
				callCount++
				return "", testErr
			},
			func(error) bool { return true },
		)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if callCount != 3 {
			t.Errorf("call count = %d, want 3 (1 initial + 2 retries)", callCount)
		}
		if !errors.Is(err, testErr) {
			t.Errorf("error should wrap original: got %v", err)
		}
	})

	t.Run("already cancelled context returns immediately", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		callCount := 0
		_, err := apierr.RetryWithBackoff(
			ctx,
			apierr.RetryConfig{MaxRetries: 5, BaseDelay: time.Second, MaxDelay: time.Minute},
			func() (string, error) {
				callCount++
				return "", errors.New("should retry")
			},
			func(error) bool { return true },
		)

		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		// First call happens, then context check on retry wait
		if callCount != 1 {
			t.Errorf("call count = %d, want 1", callCount)
		}
	})

	t.Run("context cancellation during retry stops early", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())

		callCount := 0
		_, err := apierr.RetryWithBackoff(
			ctx,
			apierr.RetryConfig{MaxRetries: 10, BaseDelay: 50 * time.Millisecond, MaxDelay: 100 * time.Millisecond},
			func() (string, error) {
				callCount++
				if callCount == 1 {
					// Cancel after first call
					go func() {
						time.Sleep(5 * time.Millisecond)
						cancel()
					}()
				}
				return "", errors.New("transient")
			},
			func(error) bool { return true },
		)

		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if callCount >= 5 {
			t.Errorf("call count = %d, should be less than 5 (cancelled early)", callCount)
		}
	})

	t.Run("negative MaxRetries normalized to 0", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		testErr := errors.New("always fails")
		_, err := apierr.RetryWithBackoff(
			context.Background(),
			apierr.RetryConfig{MaxRetries: -5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
			func() (string, error) {
				callCount++
				return "", testErr
			},
			func(error) bool { return true },
		)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if callCount != 1 {
			t.Errorf("call count = %d, want 1", callCount)
		}
	})

	t.Run("zero BaseDelay normalized to 1ms", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		_, err := apierr.RetryWithBackoff(
			context.Background(),
			apierr.RetryConfig{MaxRetries: 1, BaseDelay: 0, MaxDelay: time.Millisecond},
			func() (string, error) {
				callCount++
				if callCount < 2 {
					return "", errors.New("retry")
				}
				return "ok", nil
			},
			func(error) bool { return true },
		)

		if err != nil {
			t.Errorf("RetryWithBackoff() unexpected error: %v", err)
		}
		if callCount != 2 {
			t.Errorf("call count = %d, want 2", callCount)
		}
	})

	t.Run("zero MaxDelay normalized to BaseDelay", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		_, err := apierr.RetryWithBackoff(
			context.Background(),
			apierr.RetryConfig{MaxRetries: 1, BaseDelay: time.Millisecond, MaxDelay: 0},
			func() (string, error) {
				callCount++
				if callCount < 2 {
					return "", errors.New("retry")
				}
				return "ok", nil
			},
			func(error) bool { return true },
		)

		if err != nil {
			t.Errorf("RetryWithBackoff() unexpected error: %v", err)
		}
		if callCount != 2 {
			t.Errorf("call count = %d, want 2", callCount)
		}
	})

	t.Run("selective retry based on error type", func(t *testing.T) {
		t.Parallel()

		retryableErr := apierr.ErrRateLimit
		nonRetryableErr := apierr.ErrAuthFailed

		callCount := 0
		_, err := apierr.RetryWithBackoff(
			context.Background(),
			apierr.RetryConfig{MaxRetries: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
			func() (string, error) {
				callCount++
				if callCount == 1 {
					return "", retryableErr
				}
				return "", nonRetryableErr
			},
			func(err error) bool {
				return errors.Is(err, apierr.ErrRateLimit)
			},
		)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if callCount != 2 {
			t.Errorf("call count = %d, want 2 (1 retryable + 1 non-retryable)", callCount)
		}
		if !errors.Is(err, apierr.ErrAuthFailed) {
			t.Errorf("error = %v, want ErrAuthFailed", err)
		}
	})
}
