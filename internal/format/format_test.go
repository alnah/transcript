package format_test

// Notes:
// - Negative values are intentionally not tested: these functions are designed
//   for real durations/sizes which are always positive. Testing negatives would
//   lock in undefined behavior.
// - Very large values: we test realistic large values (24h, 10GB) not extremes
//   like math.MaxInt64 which are unrealistic for audio transcription.

import (
	"testing"
	"time"

	"github.com/alnah/transcript/internal/format"
)

// ---------------------------------------------------------------------------
// TestDuration - Formats duration as HH:MM:SS or MM:SS
// ---------------------------------------------------------------------------

func TestDuration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input time.Duration
		want  string
	}{
		// Zero value
		{name: "zero", input: 0, want: "00:00"},

		// Under a minute (MM:SS format)
		{name: "one second", input: time.Second, want: "00:01"},
		{name: "boundary: 59 seconds", input: 59 * time.Second, want: "00:59"},

		// Under an hour (MM:SS format)
		{name: "boundary: exactly 1 minute", input: time.Minute, want: "01:00"},
		{name: "typical: 5 minutes", input: 5 * time.Minute, want: "05:00"},
		{name: "mixed minutes and seconds", input: 5*time.Minute + 30*time.Second, want: "05:30"},
		{name: "boundary: 59 minutes 59 seconds", input: 59*time.Minute + 59*time.Second, want: "59:59"},

		// One hour or more (HH:MM:SS format)
		{name: "boundary: exactly 1 hour", input: time.Hour, want: "01:00:00"},
		{name: "1 hour 1 second", input: time.Hour + time.Second, want: "01:00:01"},
		{name: "typical: 1 hour 30 minutes", input: time.Hour + 30*time.Minute, want: "01:30:00"},
		{name: "full: 2 hours 15 minutes 45 seconds", input: 2*time.Hour + 15*time.Minute + 45*time.Second, want: "02:15:45"},

		// Realistic large value (long conference)
		{name: "large realistic: 24 hours", input: 24 * time.Hour, want: "24:00:00"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := format.Duration(tt.input)
			if got != tt.want {
				t.Errorf("Duration(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestDurationHuman - Formats duration for human display (2h, 30m, 1h30m, 45s)
// ---------------------------------------------------------------------------

func TestDurationHuman(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input time.Duration
		want  string
	}{
		// Seconds only (< 1 minute)
		{name: "zero", input: 0, want: "0s"},
		{name: "one second", input: time.Second, want: "1s"},
		{name: "boundary: 59 seconds", input: 59 * time.Second, want: "59s"},

		// Minutes only (>= 1 minute, < 1 hour)
		{name: "boundary: exactly 1 minute", input: time.Minute, want: "1m"},
		{name: "typical: 30 minutes", input: 30 * time.Minute, want: "30m"},
		{name: "boundary: 59 minutes", input: 59 * time.Minute, want: "59m"},

		// Hours only (>= 1 hour, no remaining minutes)
		{name: "boundary: exactly 1 hour", input: time.Hour, want: "1h"},
		{name: "typical: 2 hours", input: 2 * time.Hour, want: "2h"},

		// Hours and minutes (>= 1 hour, with remaining minutes)
		{name: "1 hour 1 minute", input: time.Hour + time.Minute, want: "1h1m"},
		{name: "typical: 1 hour 30 minutes", input: time.Hour + 30*time.Minute, want: "1h30m"},
		{name: "2 hours 45 minutes", input: 2*time.Hour + 45*time.Minute, want: "2h45m"},

		// Realistic large value
		{name: "large realistic: 24 hours", input: 24 * time.Hour, want: "24h"},

		// Seconds are truncated when >= 1 minute (documented behavior)
		{name: "minutes truncate seconds", input: time.Minute + 30*time.Second, want: "1m"},
		{name: "hours truncate seconds", input: time.Hour + 30*time.Second, want: "1h"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := format.DurationHuman(tt.input)
			if got != tt.want {
				t.Errorf("DurationHuman(%v) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestSize - Formats byte size for human display (MB, KB, bytes)
// ---------------------------------------------------------------------------

const (
	kb = 1024
	mb = 1024 * kb
	gb = 1024 * mb
)

func TestSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input int64
		want  string
	}{
		// Bytes (< 1 KB)
		{name: "zero", input: 0, want: "0 bytes"},
		{name: "one byte", input: 1, want: "1 byte"},
		{name: "typical: 512 bytes", input: 512, want: "512 bytes"},
		{name: "boundary: 1023 bytes", input: kb - 1, want: "1023 bytes"},

		// Kilobytes (>= 1 KB, < 1 MB)
		{name: "boundary: exactly 1 KB", input: kb, want: "1 KB"},
		{name: "typical: 512 KB", input: 512 * kb, want: "512 KB"},
		{name: "boundary: 1023 KB", input: mb - 1, want: "1023 KB"},

		// Megabytes (>= 1 MB)
		{name: "boundary: exactly 1 MB", input: mb, want: "1 MB"},
		{name: "typical: 50 MB", input: 50 * mb, want: "50 MB"},
		{name: "typical: 500 MB", input: 500 * mb, want: "500 MB"},

		// Realistic large value (large audio file)
		{name: "large realistic: 10 GB", input: 10 * gb, want: "10240 MB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := format.Size(tt.input)
			if got != tt.want {
				t.Errorf("Size(%d) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Fuzz Tests - Verify functions don't panic on arbitrary inputs
// ---------------------------------------------------------------------------

// FuzzDuration verifies Duration never panics and always returns non-empty.
func FuzzDuration(f *testing.F) {
	// Seed corpus with representative values
	f.Add(int64(0))
	f.Add(int64(time.Second))
	f.Add(int64(time.Minute))
	f.Add(int64(time.Hour))
	f.Add(int64(24 * time.Hour))

	f.Fuzz(func(t *testing.T, ns int64) {
		d := time.Duration(ns)
		if d < 0 {
			t.Skip("negative durations are undefined behavior")
		}
		got := format.Duration(d)
		if got == "" {
			t.Errorf("Duration(%v) returned empty string", d)
		}
	})
}

// FuzzDurationHuman verifies DurationHuman never panics and always returns non-empty.
func FuzzDurationHuman(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(time.Second))
	f.Add(int64(time.Minute))
	f.Add(int64(time.Hour))
	f.Add(int64(24 * time.Hour))

	f.Fuzz(func(t *testing.T, ns int64) {
		d := time.Duration(ns)
		if d < 0 {
			t.Skip("negative durations are undefined behavior")
		}
		got := format.DurationHuman(d)
		if got == "" {
			t.Errorf("DurationHuman(%v) returned empty string", d)
		}
	})
}

// FuzzSize verifies Size never panics and always returns non-empty.
func FuzzSize(f *testing.F) {
	f.Add(int64(0))
	f.Add(int64(1))
	f.Add(int64(kb))
	f.Add(int64(mb))
	f.Add(int64(gb))
	f.Add(int64(10 * gb))

	f.Fuzz(func(t *testing.T, bytes int64) {
		if bytes < 0 {
			t.Skip("negative sizes are undefined behavior")
		}
		got := format.Size(bytes)
		if got == "" {
			t.Errorf("Size(%d) returned empty string", bytes)
		}
	})
}
