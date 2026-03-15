package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/config"
)

// ---------------------------------------------------------------------------
// Unit tests for helper functions
// ---------------------------------------------------------------------------

func TestDefaultRecordingFilename(t *testing.T) {
	t.Parallel()

	now := func() time.Time {
		return time.Date(2026, 1, 25, 14, 30, 52, 0, time.UTC)
	}

	filename := DefaultRecordingFilename(now)

	if filename != "recording_20260125_143052.ogg" {
		t.Errorf("DefaultRecordingFilename() = %q, want %q", filename, "recording_20260125_143052.ogg")
	}
}

// ---------------------------------------------------------------------------
// Tests for runRecord
// ---------------------------------------------------------------------------

func TestRunRecord_Success(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "test.ogg")
	stderr := &syncBuffer{}

	recorder := &mockRecorder{
		RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
			// Create the output file to simulate recording
			if err := os.WriteFile(output, []byte("fake audio data"), 0644); err != nil {
				return err
			}
			return nil
		},
	}

	recorderFactory := &mockRecorderFactory{
		NewRecorderFunc: func(ffmpegPath, device string) (audio.Recorder, error) {
			return recorder, nil
		},
	}

	env := &Env{
		Stderr:          stderr,
		Getenv:          func(string) string { return "" },
		Now:             fixedTime(time.Date(2026, 1, 25, 14, 30, 52, 0, time.UTC)),
		FFmpegResolver:  &mockFFmpegResolver{},
		ConfigLoader:    &mockConfigLoader{},
		RecorderFactory: recorderFactory,
	}

	opts := recordOptions{
		duration: 30 * time.Minute,
		output:   outputPath,
	}

	err := RunRecord(context.Background(), env, opts)
	if err != nil {
		t.Fatalf("RunRecord() unexpected error: %v", err)
	}

	// Verify recorder was called
	calls := recorder.RecordCalls()
	if len(calls) != 1 {
		t.Fatalf("recorder.RecordCalls() = %d calls, want 1", len(calls))
	}
	if calls[0].Duration != 30*time.Minute {
		t.Errorf("recorder.Record() duration = %v, want %v", calls[0].Duration, 30*time.Minute)
	}
	if calls[0].Output != outputPath {
		t.Errorf("recorder.Record() output = %q, want %q", calls[0].Output, outputPath)
	}

	// Verify output contains success message
	output := stderr.String()
	if !strings.Contains(output, "Recording complete") {
		t.Errorf("RunRecord() stderr = %q, want containing %q", output, "Recording complete")
	}
}

func TestRunRecord_DefaultFilename(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	stderr := &syncBuffer{}

	recorder := &mockRecorder{
		RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
			return os.WriteFile(output, []byte("fake audio"), 0644)
		},
	}

	recorderFactory := &mockRecorderFactory{
		NewRecorderFunc: func(ffmpegPath, device string) (audio.Recorder, error) {
			return recorder, nil
		},
	}

	configLoader := &mockConfigLoader{
		LoadFunc: func() (config.Config, error) {
			return config.Config{OutputDir: outputDir}, nil
		},
	}

	fixedNow := time.Date(2026, 1, 25, 14, 30, 52, 0, time.UTC)

	env := &Env{
		Stderr:          stderr,
		Getenv:          func(string) string { return "" },
		Now:             fixedTime(fixedNow),
		FFmpegResolver:  &mockFFmpegResolver{},
		ConfigLoader:    configLoader,
		RecorderFactory: recorderFactory,
	}

	opts := recordOptions{
		duration: 10 * time.Minute,
		output:   "", // Empty - should use default
	}

	err := RunRecord(context.Background(), env, opts)
	if err != nil {
		t.Fatalf("RunRecord() unexpected error: %v", err)
	}

	// Verify the file was created with expected name
	calls := recorder.RecordCalls()
	if len(calls) != 1 {
		t.Fatalf("recorder.RecordCalls() = %d calls, want 1", len(calls))
	}

	expectedFilename := "recording_20260125_143052.ogg"
	if !strings.HasSuffix(calls[0].Output, expectedFilename) {
		t.Errorf("recorder.Record() output = %q, want ending with %q", calls[0].Output, expectedFilename)
	}
	if !strings.HasPrefix(calls[0].Output, outputDir) {
		t.Errorf("recorder.Record() output = %q, want in directory %q", calls[0].Output, outputDir)
	}
}

func TestRunRecord_OutputExists(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "existing.ogg")

	// Create existing file
	if err := os.WriteFile(outputPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("failed to create test file: %v", err)
	}

	env := &Env{
		Stderr:         &syncBuffer{},
		Getenv:         func(string) string { return "" },
		Now:            fixedTime(time.Now()),
		FFmpegResolver: &mockFFmpegResolver{},
		ConfigLoader:   &mockConfigLoader{},
	}

	opts := recordOptions{
		duration: 10 * time.Minute,
		output:   outputPath,
	}

	err := RunRecord(context.Background(), env, opts)
	if err == nil {
		t.Fatal("RunRecord() error = nil, want ErrOutputExists")
	}
	if !errors.Is(err, ErrOutputExists) {
		t.Errorf("RunRecord() error = %v, want ErrOutputExists", err)
	}
}

func TestRunRecord_ExtensionWarning(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "test.mp3") // Non-.ogg extension
	stderr := &syncBuffer{}

	recorder := &mockRecorder{
		RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
			return os.WriteFile(output, []byte("fake audio"), 0644)
		},
	}

	recorderFactory := &mockRecorderFactory{
		NewRecorderFunc: func(ffmpegPath, device string) (audio.Recorder, error) {
			return recorder, nil
		},
	}

	env := &Env{
		Stderr:          stderr,
		Getenv:          func(string) string { return "" },
		Now:             fixedTime(time.Now()),
		FFmpegResolver:  &mockFFmpegResolver{},
		ConfigLoader:    &mockConfigLoader{},
		RecorderFactory: recorderFactory,
	}

	opts := recordOptions{
		duration: 5 * time.Minute,
		output:   outputPath,
	}

	err := RunRecord(context.Background(), env, opts)
	if err != nil {
		t.Fatalf("RunRecord() unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "Warning") || !strings.Contains(output, ".mp3") {
		t.Errorf("RunRecord() stderr = %q, want containing %q and %q", output, "Warning", ".mp3")
	}
}

func TestRunRecord_FFmpegResolveFails(t *testing.T) {
	t.Parallel()

	ffmpegErr := errors.New("ffmpeg not found")
	ffmpegResolver := &mockFFmpegResolver{
		ResolveFunc: func(ctx context.Context) (string, error) {
			return "", ffmpegErr
		},
	}

	outputDir := t.TempDir()

	env := &Env{
		Stderr:         &syncBuffer{},
		Getenv:         func(string) string { return "" },
		Now:            fixedTime(time.Now()),
		FFmpegResolver: ffmpegResolver,
		ConfigLoader:   &mockConfigLoader{},
	}

	opts := recordOptions{
		duration: 5 * time.Minute,
		output:   filepath.Join(outputDir, "test.ogg"),
	}

	err := RunRecord(context.Background(), env, opts)
	if err == nil {
		t.Fatal("RunRecord() error = nil, want ffmpeg error")
	}
	if !errors.Is(err, ffmpegErr) {
		t.Errorf("RunRecord() error = %v, want %v", err, ffmpegErr)
	}
}

func TestRunRecord_LoopbackRecorder(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "loopback.ogg")

	recorder := &mockRecorder{
		RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
			return os.WriteFile(output, []byte("loopback audio"), 0644)
		},
	}

	var loopbackCalled bool
	recorderFactory := &mockRecorderFactory{
		NewLoopbackRecorderFunc: func(ctx context.Context, ffmpegPath string) (audio.Recorder, error) {
			loopbackCalled = true
			return recorder, nil
		},
	}

	env := &Env{
		Stderr:          &syncBuffer{},
		Getenv:          func(string) string { return "" },
		Now:             fixedTime(time.Now()),
		FFmpegResolver:  &mockFFmpegResolver{},
		ConfigLoader:    &mockConfigLoader{},
		RecorderFactory: recorderFactory,
	}

	opts := recordOptions{
		duration:     5 * time.Minute,
		output:       outputPath,
		systemRecord: true,
	}

	err := RunRecord(context.Background(), env, opts)
	if err != nil {
		t.Fatalf("RunRecord() unexpected error: %v", err)
	}

	if !loopbackCalled {
		t.Error("NewLoopbackRecorder called = false, want true")
	}
}

func TestRunRecord_MixRecorder(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "mix.ogg")

	recorder := &mockRecorder{
		RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
			return os.WriteFile(output, []byte("mixed audio"), 0644)
		},
	}

	var mixCalled bool
	var capturedDevice string
	recorderFactory := &mockRecorderFactory{
		NewMixRecorderFunc: func(ctx context.Context, ffmpegPath, micDevice string) (audio.Recorder, error) {
			mixCalled = true
			capturedDevice = micDevice
			return recorder, nil
		},
	}

	env := &Env{
		Stderr:          &syncBuffer{},
		Getenv:          func(string) string { return "" },
		Now:             fixedTime(time.Now()),
		FFmpegResolver:  &mockFFmpegResolver{},
		ConfigLoader:    &mockConfigLoader{},
		RecorderFactory: recorderFactory,
	}

	opts := recordOptions{
		duration: 5 * time.Minute,
		output:   outputPath,
		mix:      true,
		device:   "custom-mic",
	}

	err := RunRecord(context.Background(), env, opts)
	if err != nil {
		t.Fatalf("RunRecord() unexpected error: %v", err)
	}

	if !mixCalled {
		t.Error("NewMixRecorder called = false, want true")
	}
	if capturedDevice != "custom-mic" {
		t.Errorf("NewMixRecorder() device = %q, want %q", capturedDevice, "custom-mic")
	}
}

func TestRunRecord_RecordError(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "test.ogg")

	recordErr := errors.New("recording failed")
	recorder := &mockRecorder{
		RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
			return recordErr
		},
	}

	recorderFactory := &mockRecorderFactory{
		NewRecorderFunc: func(ffmpegPath, device string) (audio.Recorder, error) {
			return recorder, nil
		},
	}

	env := &Env{
		Stderr:          &syncBuffer{},
		Getenv:          func(string) string { return "" },
		Now:             fixedTime(time.Now()),
		FFmpegResolver:  &mockFFmpegResolver{},
		ConfigLoader:    &mockConfigLoader{},
		RecorderFactory: recorderFactory,
	}

	opts := recordOptions{
		duration: 5 * time.Minute,
		output:   outputPath,
	}

	err := RunRecord(context.Background(), env, opts)
	if err == nil {
		t.Fatal("RunRecord() error = nil, want recording error")
	}
	if !errors.Is(err, recordErr) {
		t.Errorf("RunRecord() error = %v, want %v", err, recordErr)
	}
}

func TestRunRecord_RecordingNotCreated(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "test.ogg")

	// Recorder succeeds but doesn't create the file
	recorder := &mockRecorder{
		RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
			return nil // Success but no file created
		},
	}

	recorderFactory := &mockRecorderFactory{
		NewRecorderFunc: func(ffmpegPath, device string) (audio.Recorder, error) {
			return recorder, nil
		},
	}

	env := &Env{
		Stderr:          &syncBuffer{},
		Getenv:          func(string) string { return "" },
		Now:             fixedTime(time.Now()),
		FFmpegResolver:  &mockFFmpegResolver{},
		ConfigLoader:    &mockConfigLoader{},
		RecorderFactory: recorderFactory,
	}

	opts := recordOptions{
		duration: 5 * time.Minute,
		output:   outputPath,
	}

	err := RunRecord(context.Background(), env, opts)
	if err == nil {
		t.Fatal("RunRecord() error = nil, want error when output file not created")
	}
	if !strings.Contains(err.Error(), "recording failed") {
		t.Errorf("RunRecord() error = %q, want containing %q", err.Error(), "recording failed")
	}
}

func TestRunRecord_ContextCanceled(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "test.ogg")
	stderr := &syncBuffer{}

	ctx, cancel := context.WithCancel(context.Background())

	recorder := &mockRecorder{
		RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
			// Create file then simulate interrupt
			if err := os.WriteFile(output, []byte("partial audio"), 0644); err != nil {
				return err
			}
			cancel() // Simulate interrupt
			return ctx.Err()
		},
	}

	recorderFactory := &mockRecorderFactory{
		NewRecorderFunc: func(ffmpegPath, device string) (audio.Recorder, error) {
			return recorder, nil
		},
	}

	env := &Env{
		Stderr:          stderr,
		Getenv:          func(string) string { return "" },
		Now:             fixedTime(time.Now()),
		FFmpegResolver:  &mockFFmpegResolver{},
		ConfigLoader:    &mockConfigLoader{},
		RecorderFactory: recorderFactory,
	}

	opts := recordOptions{
		duration: 5 * time.Minute,
		output:   outputPath,
	}

	err := RunRecord(ctx, env, opts)
	// Should still succeed because file was created
	if err != nil {
		t.Fatalf("RunRecord() unexpected error on interrupt with valid file: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "Interrupted") {
		t.Errorf("RunRecord() stderr = %q, want containing %q", output, "Interrupted")
	}
}

func TestRunRecord_ConfigLoadWarning(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "test.ogg")
	stderr := &syncBuffer{}

	recorder := &mockRecorder{
		RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
			return os.WriteFile(output, []byte("audio"), 0644)
		},
	}

	recorderFactory := &mockRecorderFactory{
		NewRecorderFunc: func(ffmpegPath, device string) (audio.Recorder, error) {
			return recorder, nil
		},
	}

	configLoader := &mockConfigLoader{
		LoadFunc: func() (config.Config, error) {
			return config.Config{}, errors.New("config file corrupted")
		},
	}

	env := &Env{
		Stderr:          stderr,
		Getenv:          func(string) string { return "" },
		Now:             fixedTime(time.Now()),
		FFmpegResolver:  &mockFFmpegResolver{},
		ConfigLoader:    configLoader,
		RecorderFactory: recorderFactory,
	}

	opts := recordOptions{
		duration: 5 * time.Minute,
		output:   outputPath,
	}

	// Should succeed despite config error (just warns)
	err := RunRecord(context.Background(), env, opts)
	if err != nil {
		t.Fatalf("RunRecord() unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "Warning") || !strings.Contains(output, "config") {
		t.Errorf("RunRecord() stderr = %q, want containing %q and %q", output, "Warning", "config")
	}
}

// ---------------------------------------------------------------------------
// Tests for RecordCmd (Cobra integration)
// ---------------------------------------------------------------------------

func TestRecordCmd_RequiresDuration(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := RecordCmd(env)

	// Execute without duration flag
	cmd.SetArgs([]string{})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("cmd.Execute() error = nil, want error when duration not provided")
	}
	if !strings.Contains(err.Error(), "duration") {
		t.Errorf("cmd.Execute() error = %q, want containing %q", err.Error(), "duration")
	}
}

func TestRecordCmd_InvalidDuration(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := RecordCmd(env)

	cmd.SetArgs([]string{"-d", "invalid"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("cmd.Execute() error = nil, want ErrInvalidDuration")
	}
	if !errors.Is(err, ErrInvalidDuration) {
		t.Errorf("cmd.Execute() error = %v, want ErrInvalidDuration", err)
	}
}

func TestRecordCmd_NegativeDuration(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := RecordCmd(env)

	cmd.SetArgs([]string{"-d", "-5m"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("cmd.Execute() error = nil, want ErrInvalidDuration")
	}
	if !errors.Is(err, ErrInvalidDuration) {
		t.Errorf("cmd.Execute() error = %v, want ErrInvalidDuration", err)
	}
}

func TestRecordCmd_ZeroDuration(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := RecordCmd(env)

	cmd.SetArgs([]string{"-d", "0s"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("cmd.Execute() error = nil, want ErrInvalidDuration")
	}
	if !errors.Is(err, ErrInvalidDuration) {
		t.Errorf("cmd.Execute() error = %v, want ErrInvalidDuration", err)
	}
}

func TestRecordCmd_MutuallyExclusiveFlags(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := RecordCmd(env)

	cmd.SetArgs([]string{"-d", "30m", "--system-record", "--mix"})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("cmd.Execute() error = nil, want mutually exclusive flags error")
	}
	// Cobra handles this with specific error message
	if !strings.Contains(err.Error(), "cannot be used") && !strings.Contains(err.Error(), "none of the others") {
		t.Errorf("cmd.Execute() error = %q, want containing %q or %q", err.Error(), "cannot be used", "none of the others")
	}
}
