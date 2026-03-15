package audio_test

// Notes:
// - Tests use dependency injection for shell command mocking
// - OS-specific detection functions tested via mocks
// - Error wrapping and help messages are tested for user-facing quality

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/alnah/transcript/internal/audio"
)

// ---------------------------------------------------------------------------
// ExtractDShowDeviceName - Windows device name extraction
// ---------------------------------------------------------------------------

func TestExtractDShowDeviceName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		stderr      string
		partialName string
		want        string
	}{
		{
			name: "extracts quoted device name",
			stderr: `[dshow @ 0x000001] DirectShow audio devices
[dshow @ 0x000001]  "Stereo Mix (Realtek High Definition Audio)"`,
			partialName: "Stereo Mix",
			want:        "Stereo Mix (Realtek High Definition Audio)",
		},
		{
			name: "extracts from multiple lines",
			stderr: `[dshow @ 0x000001]  "Microphone (Realtek)"
[dshow @ 0x000001]  "Stereo Mix (Realtek)"`,
			partialName: "Stereo Mix",
			want:        "Stereo Mix (Realtek)",
		},
		{
			name:        "not found returns empty",
			stderr:      `[dshow @ 0x000001]  "Microphone"`,
			partialName: "Stereo Mix",
			want:        "",
		},
		{
			name:        "empty stderr",
			stderr:      "",
			partialName: "Stereo Mix",
			want:        "",
		},
		{
			name:        "no quotes returns empty",
			stderr:      "[dshow @ 0x000001] Stereo Mix without quotes",
			partialName: "Stereo Mix",
			want:        "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := audio.ExtractDShowDeviceName(tt.stderr, tt.partialName)
			if got != tt.want {
				t.Errorf("ExtractDShowDeviceName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LoopbackError - Error wrapping and messages
// ---------------------------------------------------------------------------

func TestLoopbackError(t *testing.T) {
	t.Parallel()

	err := audio.NewLoopbackError(audio.ErrLoopbackNotFound, "Install BlackHole")

	// Test Error() includes both wrapped error and help
	errStr := err.Error()
	if !strings.Contains(errStr, "loopback device not found") {
		t.Errorf("Error() missing wrapped error message: %q", errStr)
	}
	if !strings.Contains(errStr, "Install BlackHole") {
		t.Errorf("Error() missing help message: %q", errStr)
	}

	// Test Unwrap() for errors.Is compatibility
	if !errors.Is(err, audio.ErrLoopbackNotFound) {
		t.Error("errors.Is(err, ErrLoopbackNotFound) = false, want true")
	}
}

// ---------------------------------------------------------------------------
// LoopbackInstallInstructions - Help message quality
// ---------------------------------------------------------------------------

func TestLoopbackInstallInstructionsDarwin(t *testing.T) {
	t.Parallel()

	instructions := audio.LoopbackInstallInstructionsDarwin()

	// Should mention BlackHole
	if !strings.Contains(instructions, "BlackHole") {
		t.Error("Darwin instructions missing BlackHole")
	}

	// Should include brew install command
	if !strings.Contains(instructions, "brew install") {
		t.Error("Darwin instructions missing brew install command")
	}

	// Should mention Multi-Output Device setup
	if !strings.Contains(instructions, "Multi-Output") {
		t.Error("Darwin instructions missing Multi-Output Device setup")
	}
}

func TestLoopbackInstallInstructionsLinux(t *testing.T) {
	t.Parallel()

	instructions := audio.LoopbackInstallInstructionsLinux()

	// Should mention PulseAudio or PipeWire
	if !strings.Contains(instructions, "PulseAudio") && !strings.Contains(instructions, "PipeWire") {
		t.Error("Linux instructions missing PulseAudio/PipeWire")
	}

	// Should include apt/dnf install commands
	if !strings.Contains(instructions, "apt install") && !strings.Contains(instructions, "dnf install") {
		t.Error("Linux instructions missing package manager commands")
	}
}

func TestLoopbackInstallInstructionsWindows(t *testing.T) {
	t.Parallel()

	instructions := audio.LoopbackInstallInstructionsWindows()

	// Should mention Stereo Mix
	if !strings.Contains(instructions, "Stereo Mix") {
		t.Error("Windows instructions missing Stereo Mix")
	}

	// Should mention VB-Audio as alternative
	if !strings.Contains(instructions, "VB-Audio") {
		t.Error("Windows instructions missing VB-Audio alternative")
	}
}

// ---------------------------------------------------------------------------
// DetectLoopbackLinux - Linux loopback detection with mocks
// ---------------------------------------------------------------------------

func TestDetectLoopbackLinuxWithRunner(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		runner     *mockShellRunner
		wantDevice bool
		wantErr    bool
	}{
		{
			name: "successful detection with pactl",
			runner: &mockShellRunner{
				outputs: map[string]mockOutput{
					"pactl": {output: []byte("alsa_output.pci-0000_00_1f.3.analog-stereo"), err: nil},
				},
			},
			wantDevice: true,
			wantErr:    false,
		},
		{
			name: "pactl fails but pipewire available",
			runner: &mockShellRunner{
				outputs: map[string]mockOutput{
					"pactl":  {output: nil, err: errors.New("not found")},
					"pw-cli": {output: []byte("info"), err: nil},
				},
			},
			wantDevice: false,
			wantErr:    true, // suggests installing pactl
		},
		{
			name: "both pactl and pipewire fail",
			runner: &mockShellRunner{
				outputs: map[string]mockOutput{
					"pactl":  {output: nil, err: errors.New("not found")},
					"pw-cli": {output: nil, err: errors.New("not found")},
				},
			},
			wantDevice: false,
			wantErr:    true,
		},
		{
			name: "pactl returns empty sink name",
			runner: &mockShellRunner{
				outputs: map[string]mockOutput{
					"pactl": {output: []byte(""), err: nil},
				},
			},
			wantDevice: false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			device, err := audio.DetectLoopbackLinuxWithRunner(context.Background(), tt.runner)

			if (err != nil) != tt.wantErr {
				t.Fatalf("DetectLoopbackLinuxWithRunner() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.wantDevice {
				if device == nil {
					t.Errorf("DetectLoopbackLinuxWithRunner() returned nil device, want non-nil")
				}
			}

			if tt.wantErr && err != nil {
				// Verify error wraps ErrLoopbackNotFound
				if !errors.Is(err, audio.ErrLoopbackNotFound) {
					t.Errorf("DetectLoopbackLinuxWithRunner() error wrapping: errors.Is(err, ErrLoopbackNotFound) = false, want true")
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Mock shell runner for testing
// ---------------------------------------------------------------------------

type mockOutput struct {
	output []byte
	err    error
}

type mockShellRunner struct {
	outputs map[string]mockOutput
}

func (m *mockShellRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	if out, ok := m.outputs[name]; ok {
		return out.output, out.err
	}
	return nil, errors.New("command not mocked: " + name)
}
