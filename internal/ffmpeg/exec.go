package ffmpeg

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"
)

// RunGraceful executes FFmpeg with graceful shutdown on context cancellation.
// When ctx is canceled, it sends 'q' to stdin to allow FFmpeg to finalize the file
// properly (write headers, close container), then waits up to timeout before killing.
// This approach works cross-platform (Windows/macOS/Linux) unlike SIGTERM.
func RunGraceful(ctx context.Context, ffmpegPath string, args []string, timeout time.Duration) error {
	// #nosec G204 -- ffmpegPath is resolved by internal resolver or explicit FFMPEG_PATH.
	cmd := exec.Command(ffmpegPath, args...)

	// Create stdin pipe for graceful shutdown via 'q' command.
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("create stdin pipe: %w", err)
	}

	// Capture stderr for error messages (FFmpeg writes most output to stderr).
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		_ = stdin.Close() // Clean up pipe on start failure
		return fmt.Errorf("start ffmpeg: %w", err)
	}

	// Channel to receive the result of cmd.Wait().
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()

	select {
	case err := <-done:
		// FFmpeg completed normally (or with error).
		if err != nil {
			return fmt.Errorf("ffmpeg: %w\nOutput: %s", err, stderr.String())
		}
		return nil

	case <-ctx.Done():
		// Context canceled - initiate graceful shutdown.
		// Send 'q' to FFmpeg stdin to request graceful exit.
		_, _ = io.WriteString(stdin, "q")
		_ = stdin.Close()

		// Wait for FFmpeg to exit gracefully or timeout.
		select {
		case err := <-done:
			// FFmpeg exited after receiving 'q'.
			if err != nil {
				// Exit code != 0 is expected when interrupted, check if file was written.
				// FFmpeg returns error on interrupt but file should be valid.
				return nil
			}
			return nil

		case <-time.After(timeout):
			// Timeout reached - force kill.
			_ = cmd.Process.Kill()
			<-done // Wait for process to be reaped.
			return fmt.Errorf("%w: killed after %v", ErrTimeout, timeout)
		}
	}
}

// ---------------------------------------------------------------------------
// Executor - testable FFmpeg execution with dependency injection
// ---------------------------------------------------------------------------

// runOutputFn is the function type for running a command and capturing output.
type runOutputFn func(ctx context.Context, path string, args []string) (string, error)

// Executor runs FFmpeg commands with injectable dependencies.
type Executor struct {
	runOutput runOutputFn
}

// ExecutorOption configures an Executor.
type ExecutorOption func(*Executor)

// WithRunOutput sets a custom runOutput function (for testing).
func WithRunOutput(fn runOutputFn) ExecutorOption {
	return func(e *Executor) { e.runOutput = fn }
}

// NewExecutor creates an Executor with the given options.
func NewExecutor(opts ...ExecutorOption) *Executor {
	e := &Executor{
		runOutput: defaultRunOutput,
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// RunOutput executes FFmpeg and captures its stderr output.
// FFmpeg writes most diagnostic output (including device lists, probe info) to stderr.
func (e *Executor) RunOutput(ctx context.Context, ffmpegPath string, args []string) (string, error) {
	return e.runOutput(ctx, ffmpegPath, args)
}

// defaultRunOutput is the production implementation.
// Returns stderr output even when the command fails, since FFmpeg often returns
// non-zero exit codes for valid operations (e.g., -list_devices returns 1).
// The error is returned for debugging but callers typically ignore it.
func defaultRunOutput(ctx context.Context, ffmpegPath string, args []string) (string, error) {
	// #nosec G204 -- ffmpegPath is resolved by internal resolver or explicit FFMPEG_PATH.
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	err := cmd.Run()

	// Return stderr output regardless of error - it contains the useful data.
	// FFmpeg writes diagnostic output to stderr even on "failure".
	return stderr.String(), err
}

// ---------------------------------------------------------------------------
// Package-level functions - backward compatible facade
// ---------------------------------------------------------------------------

var (
	defaultExecutor     *Executor
	defaultExecutorOnce sync.Once
)

// getDefaultExecutor returns the lazily-initialized default executor.
func getDefaultExecutor() *Executor {
	defaultExecutorOnce.Do(func() {
		defaultExecutor = NewExecutor()
	})
	return defaultExecutor
}

// RunOutput executes FFmpeg and captures its stderr output.
// This is a backward-compatible facade for the Executor.RunOutput method.
func RunOutput(ctx context.Context, ffmpegPath string, args []string) (string, error) {
	return getDefaultExecutor().RunOutput(ctx, ffmpegPath, args)
}
