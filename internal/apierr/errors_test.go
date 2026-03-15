package apierr_test

// Coverage Notes:
// - Tests verify sentinel error identity with errors.Is.
// - Tests verify wrapping behavior with fmt.Errorf("%s: %w", ...).
// - All sentinels are tested: ErrRateLimit, ErrQuotaExceeded, ErrTimeout, ErrAuthFailed, ErrBadRequest.

import (
	"errors"
	"fmt"
	"testing"

	"github.com/alnah/transcript/internal/apierr"
)

// ---------------------------------------------------------------------------
// TestSentinelErrorIdentity - errors.Is matches for all sentinels
// ---------------------------------------------------------------------------

func TestSentinelErrorIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sentinel error
	}{
		{"ErrRateLimit", apierr.ErrRateLimit},
		{"ErrQuotaExceeded", apierr.ErrQuotaExceeded},
		{"ErrTimeout", apierr.ErrTimeout},
		{"ErrAuthFailed", apierr.ErrAuthFailed},
		{"ErrBadRequest", apierr.ErrBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if !errors.Is(tt.sentinel, tt.sentinel) {
				t.Errorf("errors.Is(%v, %v) = false, want true", tt.sentinel, tt.sentinel)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestSentinelErrorWrapping - wrapped errors still match with errors.Is
// ---------------------------------------------------------------------------

func TestSentinelErrorWrapping(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		sentinel error
	}{
		{"wrapped ErrRateLimit", apierr.ErrRateLimit},
		{"wrapped ErrQuotaExceeded", apierr.ErrQuotaExceeded},
		{"wrapped ErrTimeout", apierr.ErrTimeout},
		{"wrapped ErrAuthFailed", apierr.ErrAuthFailed},
		{"wrapped ErrBadRequest", apierr.ErrBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			wrapped := fmt.Errorf("some context: %w", tt.sentinel)

			if !errors.Is(wrapped, tt.sentinel) {
				t.Errorf("errors.Is(wrapped, %v) = false, want true", tt.sentinel)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestSentinelErrorDistinct - sentinels are distinct from each other
// ---------------------------------------------------------------------------

func TestSentinelErrorDistinct(t *testing.T) {
	t.Parallel()

	sentinels := []error{
		apierr.ErrRateLimit,
		apierr.ErrQuotaExceeded,
		apierr.ErrTimeout,
		apierr.ErrAuthFailed,
		apierr.ErrBadRequest,
	}

	for i, a := range sentinels {
		for j, b := range sentinels {
			if i == j {
				continue
			}
			t.Run(fmt.Sprintf("%v_is_not_%v", a, b), func(t *testing.T) {
				t.Parallel()

				if errors.Is(a, b) {
					t.Errorf("errors.Is(%v, %v) = true, want false", a, b)
				}
			})
		}
	}
}
