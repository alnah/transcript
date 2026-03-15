//go:build integration

package audio_test

// Notes:
// - These tests require a loopback audio device (BlackHole on macOS, PulseAudio monitor on Linux)
// - Tests gracefully skip when no loopback device is available (CI environments)
// - Tests verify the fix for nil pointer panic in loopback/mix recorder constructors

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/alnah/transcript/internal/audio"
)

// ---------------------------------------------------------------------------
// Integration: NewFFmpegLoopbackRecorder - Regression test for nil ffmpegRunner
// ---------------------------------------------------------------------------

// TestNewFFmpegLoopbackRecorder_Integration verifies that loopback recorders
// are properly initialized and don't panic when Record() is called.
//
// This is a regression test for a bug where NewFFmpegLoopbackRecorder did not
// initialize ffmpegRunner, causing nil pointer dereference on Record().
//
// The test skips gracefully if:
// - FFmpeg is not installed
// - No loopback device is available (e.g., CI environment)
func TestNewFFmpegLoopbackRecorder_Integration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find FFmpeg
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("skipping: ffmpeg not found in PATH")
	}

	// Try to create a loopback recorder - this will fail if no loopback device
	rec, err := audio.NewFFmpegLoopbackRecorder(ctx, ffmpegPath)
	if err != nil {
		t.Skipf("skipping: no loopback device available: %v", err)
	}

	// Create temp output file
	tmpDir := t.TempDir()
	output := filepath.Join(tmpDir, "loopback_test.ogg")

	// Record for 1 second - this would panic before the fix
	// We use a short duration and cancel early to avoid waiting
	recordCtx, recordCancel := context.WithTimeout(ctx, 1*time.Second)
	defer recordCancel()

	// The key assertion: this should NOT panic
	// Before the fix, this panicked with: nil pointer dereference at r.ffmpegRunner.RunGraceful
	err = rec.Record(recordCtx, 2*time.Second, output)

	// We expect either:
	// - nil error (recording succeeded)
	// - context deadline exceeded (we cancelled early)
	// - some ffmpeg error (device busy, permission denied, etc.)
	// What we do NOT expect: panic

	if err != nil && ctx.Err() == nil {
		// Log the error but don't fail - the point is it didn't panic
		t.Logf("Record() returned error (expected in some environments): %v", err)
	}

	// Verify file was created (may be empty or partial due to short duration)
	if _, statErr := os.Stat(output); statErr == nil {
		t.Logf("Output file created: %s", output)
	}
}

// ---------------------------------------------------------------------------
// Integration: NewFFmpegMixRecorder - Regression test for nil ffmpegRunner
// ---------------------------------------------------------------------------

// TestNewFFmpegMixRecorder_Integration verifies that mix recorders
// are properly initialized and don't panic when Record() is called.
//
// This is a regression test for the same bug as TestNewFFmpegLoopbackRecorder_Integration.
func TestNewFFmpegMixRecorder_Integration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Find FFmpeg
	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("skipping: ffmpeg not found in PATH")
	}

	// Try to create a mix recorder - this will fail if no loopback device
	rec, err := audio.NewFFmpegMixRecorder(ctx, ffmpegPath, "")
	if err != nil {
		t.Skipf("skipping: no loopback device available: %v", err)
	}

	// Create temp output file
	tmpDir := t.TempDir()
	output := filepath.Join(tmpDir, "mix_test.ogg")

	// Record for 1 second - this would panic before the fix
	recordCtx, recordCancel := context.WithTimeout(ctx, 1*time.Second)
	defer recordCancel()

	// The key assertion: this should NOT panic
	err = rec.Record(recordCtx, 2*time.Second, output)

	if err != nil && ctx.Err() == nil {
		t.Logf("Record() returned error (expected in some environments): %v", err)
	}

	if _, statErr := os.Stat(output); statErr == nil {
		t.Logf("Output file created: %s", output)
	}
}

// ---------------------------------------------------------------------------
// Integration: Loopback recorder accepts and applies options
// ---------------------------------------------------------------------------

// TestNewFFmpegLoopbackRecorder_AcceptsOptions verifies that the loopback
// recorder constructor properly accepts and applies RecorderOption arguments.
// This tests the API change that added variadic options support.
func TestNewFFmpegLoopbackRecorder_AcceptsOptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("skipping: ffmpeg not found in PATH")
	}

	// Track if our mock was called
	mockCalled := false
	mockRunner := &testFFmpegRunner{
		runGracefulFunc: func(ctx context.Context, path string, args []string, timeout time.Duration) error {
			mockCalled = true
			return nil
		},
	}

	// Create recorder with custom option
	rec, err := audio.NewFFmpegLoopbackRecorder(ctx, ffmpegPath, audio.ExportedWithFFmpegRunner(mockRunner))
	if err != nil {
		t.Skipf("skipping: no loopback device available: %v", err)
	}

	// Record should use our mock
	tmpDir := t.TempDir()
	output := filepath.Join(tmpDir, "option_test.ogg")

	_ = rec.Record(ctx, 1*time.Second, output)

	if !mockCalled {
		t.Error("NewFFmpegLoopbackRecorder() with custom runner: mock.RunGraceful called = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Integration: Mix recorder accepts and applies options
// ---------------------------------------------------------------------------

// TestNewFFmpegMixRecorder_AcceptsOptions verifies that the mix
// recorder constructor properly accepts and applies RecorderOption arguments.
func TestNewFFmpegMixRecorder_AcceptsOptions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	ffmpegPath, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("skipping: ffmpeg not found in PATH")
	}

	mockCalled := false
	mockRunner := &testFFmpegRunner{
		runGracefulFunc: func(ctx context.Context, path string, args []string, timeout time.Duration) error {
			mockCalled = true
			return nil
		},
	}

	// Use explicit mic device ":0" to avoid auto-detection which would call
	// RunOutput (not mocked here). The test verifies option injection, not device detection.
	rec, err := audio.NewFFmpegMixRecorder(ctx, ffmpegPath, ":0", audio.ExportedWithFFmpegRunner(mockRunner))
	if err != nil {
		t.Skipf("skipping: no loopback device available: %v", err)
	}

	tmpDir := t.TempDir()
	output := filepath.Join(tmpDir, "mix_option_test.ogg")

	_ = rec.Record(ctx, 1*time.Second, output)

	if !mockCalled {
		t.Error("NewFFmpegMixRecorder() with custom runner: mock.RunGraceful called = false, want true")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// testFFmpegRunner implements audio.FFmpegRunner for integration testing.
type testFFmpegRunner struct {
	runOutputFunc   func(ctx context.Context, ffmpegPath string, args []string) (string, error)
	runGracefulFunc func(ctx context.Context, ffmpegPath string, args []string, timeout time.Duration) error
}

func (r *testFFmpegRunner) RunOutput(ctx context.Context, ffmpegPath string, args []string) (string, error) {
	if r.runOutputFunc != nil {
		return r.runOutputFunc(ctx, ffmpegPath, args)
	}
	return "", nil
}

func (r *testFFmpegRunner) RunGraceful(ctx context.Context, ffmpegPath string, args []string, timeout time.Duration) error {
	if r.runGracefulFunc != nil {
		return r.runGracefulFunc(ctx, ffmpegPath, args, timeout)
	}
	return nil
}
