package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alnah/transcript/internal/audio"
)

// ---------------------------------------------------------------------------
// Tests for runListDevices
// ---------------------------------------------------------------------------

func TestRunListDevices_Success(t *testing.T) {
	t.Parallel()

	stderr := &syncBuffer{}

	listerFactory := &mockDeviceListerFactory{
		mockDeviceLister: &mockDeviceLister{
			ListDevicesFunc: func(ctx context.Context) ([]string, error) {
				return []string{
					":1\tMacBook Pro Microphone",
					":0\tAirBeamTV Audio",
					":2\tBlackHole 2ch",
				}, nil
			},
		},
	}

	env := &Env{
		Stderr:              stderr,
		FFmpegResolver:      &mockFFmpegResolver{},
		DeviceListerFactory: listerFactory,
	}

	err := RunListDevices(context.Background(), env)
	if err != nil {
		t.Fatalf("RunListDevices() unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "MacBook Pro Microphone") {
		t.Errorf("output missing device name: %q", output)
	}
	if !strings.Contains(output, ":1") {
		t.Errorf("output missing device index: %q", output)
	}

	// Verify all three devices appear
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 3 {
		t.Errorf("expected 3 lines, got %d: %q", len(lines), output)
	}
}

func TestRunListDevices_NoDevices(t *testing.T) {
	t.Parallel()

	stderr := &syncBuffer{}

	listerFactory := &mockDeviceListerFactory{
		mockDeviceLister: &mockDeviceLister{
			ListDevicesFunc: func(ctx context.Context) ([]string, error) {
				return nil, nil
			},
		},
	}

	env := &Env{
		Stderr:              stderr,
		FFmpegResolver:      &mockFFmpegResolver{},
		DeviceListerFactory: listerFactory,
	}

	err := RunListDevices(context.Background(), env)
	if err != nil {
		t.Fatalf("RunListDevices() unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "No audio input devices found") {
		t.Errorf("expected 'No audio input devices found' message, got: %q", output)
	}
}

func TestRunListDevices_FFmpegResolveFails(t *testing.T) {
	t.Parallel()

	ffmpegErr := errors.New("ffmpeg not found")

	env := &Env{
		Stderr: &syncBuffer{},
		FFmpegResolver: &mockFFmpegResolver{
			ResolveFunc: func(ctx context.Context) (string, error) {
				return "", ffmpegErr
			},
		},
		DeviceListerFactory: &mockDeviceListerFactory{},
	}

	err := RunListDevices(context.Background(), env)
	if err == nil {
		t.Fatal("RunListDevices() error = nil, want ffmpeg error")
	}
	if !errors.Is(err, ffmpegErr) {
		t.Errorf("RunListDevices() error = %v, want %v", err, ffmpegErr)
	}
}

func TestRunListDevices_ListerError(t *testing.T) {
	t.Parallel()

	listErr := errors.New("device listing failed")

	listerFactory := &mockDeviceListerFactory{
		mockDeviceLister: &mockDeviceLister{
			ListDevicesFunc: func(ctx context.Context) ([]string, error) {
				return nil, listErr
			},
		},
	}

	env := &Env{
		Stderr:              &syncBuffer{},
		FFmpegResolver:      &mockFFmpegResolver{},
		DeviceListerFactory: listerFactory,
	}

	err := RunListDevices(context.Background(), env)
	if err == nil {
		t.Fatal("RunListDevices() error = nil, want error")
	}
	if !errors.Is(err, listErr) {
		t.Errorf("RunListDevices() error = %v, want %v", err, listErr)
	}
}

// ---------------------------------------------------------------------------
// Tests for DevicesCmd (Cobra integration)
// ---------------------------------------------------------------------------

func TestDevicesCmd_Success(t *testing.T) {
	t.Parallel()

	stderr := &syncBuffer{}

	listerFactory := &mockDeviceListerFactory{
		mockDeviceLister: &mockDeviceLister{
			ListDevicesFunc: func(ctx context.Context) ([]string, error) {
				return []string{"Microphone (Realtek)"}, nil
			},
		},
	}

	env := &Env{
		Stderr:              stderr,
		FFmpegResolver:      &mockFFmpegResolver{},
		DeviceListerFactory: listerFactory,
	}

	cmd := DevicesCmd(env)
	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err != nil {
		t.Fatalf("DevicesCmd.Execute() unexpected error: %v", err)
	}

	output := stderr.String()
	if !strings.Contains(output, "Microphone (Realtek)") {
		t.Errorf("output missing device: %q", output)
	}
}

func TestDevicesCmd_PassesFFmpegPath(t *testing.T) {
	t.Parallel()

	var capturedPath string
	listerFactory := &mockDeviceListerFactory{
		NewDeviceListerFunc: func(ffmpegPath string) (audio.DeviceLister, error) {
			capturedPath = ffmpegPath
			return &mockDeviceLister{}, nil
		},
	}

	env := &Env{
		Stderr: &syncBuffer{},
		FFmpegResolver: &mockFFmpegResolver{
			ResolveFunc: func(ctx context.Context) (string, error) {
				return "/custom/ffmpeg", nil
			},
		},
		DeviceListerFactory: listerFactory,
	}

	cmd := DevicesCmd(env)
	cmd.SetArgs([]string{})
	_ = cmd.Execute()

	if capturedPath != "/custom/ffmpeg" {
		t.Errorf("DeviceLister received ffmpegPath = %q, want %q", capturedPath, "/custom/ffmpeg")
	}
}
