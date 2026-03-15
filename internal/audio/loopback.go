package audio

import (
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/alnah/transcript/internal/ffmpeg"
)

// shellCommandRunner executes shell commands.
type shellCommandRunner interface {
	Output(ctx context.Context, name string, args ...string) ([]byte, error)
}

// osShellRunner implements shellCommandRunner using os/exec.
type osShellRunner struct{}

func (osShellRunner) Output(ctx context.Context, name string, args ...string) ([]byte, error) {
	// #nosec G204 -- commands are fixed probes (pactl/pw-cli) from internal code paths.
	return exec.CommandContext(ctx, name, args...).Output()
}

// defaultShellRunner is the default shell command runner.
var defaultShellRunner shellCommandRunner = osShellRunner{}

// CaptureMode defines what audio source to capture.
type CaptureMode int

const (
	// CaptureMicrophone captures from the default microphone input.
	CaptureMicrophone CaptureMode = iota
	// CaptureLoopback captures system audio output (what you hear).
	CaptureLoopback
	// CaptureMix captures both microphone and system audio mixed together.
	CaptureMix
)

// loopbackDevice holds information about a detected loopback device.
type loopbackDevice struct {
	name   string // Device name for FFmpeg -i argument
	format string // FFmpeg input format (avfoundation, pulse, dshow)
}

// DetectLoopbackDevice attempts to find a loopback device for the current OS.
// Returns ErrLoopbackNotFound with installation instructions if not found.
func DetectLoopbackDevice(ctx context.Context, ffmpegPath string) (*loopbackDevice, error) {
	switch runtime.GOOS {
	case "darwin":
		return detectLoopbackDarwin(ctx, ffmpegPath)
	case "linux":
		return detectLoopbackLinux(ctx)
	case "windows":
		return detectLoopbackWindows(ctx, ffmpegPath)
	default:
		return nil, &loopbackError{
			wrapped: ErrLoopbackNotFound,
			help:    fmt.Sprintf("loopback capture not supported on %s", runtime.GOOS),
		}
	}
}

// loopbackError wraps ErrLoopbackNotFound with installation instructions.
type loopbackError struct {
	wrapped error
	help    string
}

func (e *loopbackError) Error() string {
	return fmt.Sprintf("%v\n\n%s", e.wrapped, e.help)
}

func (e *loopbackError) Unwrap() error {
	return e.wrapped
}

// detectLoopbackDarwin detects BlackHole on macOS.
// BlackHole creates a virtual audio device that appears in AVFoundation.
func detectLoopbackDarwin(ctx context.Context, ffmpegPath string) (*loopbackDevice, error) {
	// List available audio devices
	args := []string{"-f", "avfoundation", "-list_devices", "true", "-i", ""}
	stderr, err := ffmpeg.RunOutput(ctx, ffmpegPath, args)
	if err != nil {
		// FFmpeg returns error when listing devices, but stderr has the info
		if stderr == "" {
			return nil, &loopbackError{
				wrapped: ErrLoopbackNotFound,
				help:    loopbackInstallInstructionsDarwin(),
			}
		}
	}

	// Look for BlackHole in the device list
	// Supported variants: "BlackHole 2ch", "BlackHole 16ch", "BlackHole 64ch"
	blackholeNames := []string{"BlackHole 2ch", "BlackHole 16ch", "BlackHole 64ch"}
	for _, name := range blackholeNames {
		if strings.Contains(stderr, name) {
			return &loopbackDevice{
				name:   ":" + name,
				format: "avfoundation",
			}, nil
		}
	}

	return nil, &loopbackError{
		wrapped: ErrLoopbackNotFound,
		help:    loopbackInstallInstructionsDarwin(),
	}
}

// detectLoopbackLinux detects PulseAudio/PipeWire monitor device.
// This is native on Linux - no additional driver needed.
func detectLoopbackLinux(ctx context.Context) (*loopbackDevice, error) {
	return detectLoopbackLinuxWithRunner(ctx, defaultShellRunner)
}

// detectLoopbackLinuxWithRunner is the testable version of detectLoopbackLinux.
func detectLoopbackLinuxWithRunner(ctx context.Context, runner shellCommandRunner) (*loopbackDevice, error) {
	// Try to get the default sink name from PulseAudio/PipeWire
	// The monitor device is named "<sink-name>.monitor"
	output, err := runner.Output(ctx, "pactl", "get-default-sink")
	if err != nil {
		// pactl not available, try checking for PipeWire
		if _, pwErr := runner.Output(ctx, "pw-cli", "info", "0"); pwErr != nil {
			return nil, &loopbackError{
				wrapped: ErrLoopbackNotFound,
				help:    loopbackInstallInstructionsLinux(),
			}
		}
		// PipeWire is available but pactl isn't - suggest installing pactl
		return nil, &loopbackError{
			wrapped: ErrLoopbackNotFound,
			help:    "PipeWire detected but pactl not found.\nInstall with: sudo apt install pulseaudio-utils",
		}
	}

	sinkName := strings.TrimSpace(string(output))
	if sinkName == "" {
		return nil, &loopbackError{
			wrapped: ErrLoopbackNotFound,
			help:    loopbackInstallInstructionsLinux(),
		}
	}

	// The monitor device is the sink name with ".monitor" appended
	monitorName := sinkName + ".monitor"

	return &loopbackDevice{
		name:   monitorName,
		format: "pulse",
	}, nil
}

// detectLoopbackWindows detects loopback devices on Windows.
// Priority: 1) Stereo Mix (native), 2) virtual-audio-capturer (requires install)
func detectLoopbackWindows(ctx context.Context, ffmpegPath string) (*loopbackDevice, error) {
	// List available audio devices
	args := []string{"-f", "dshow", "-list_devices", "true", "-i", "dummy"}
	stderr, err := ffmpeg.RunOutput(ctx, ffmpegPath, args)
	if err != nil && stderr == "" {
		return nil, &loopbackError{
			wrapped: ErrLoopbackNotFound,
			help:    loopbackInstallInstructionsWindows(),
		}
	}

	// Priority 1: Look for Stereo Mix (native, no install required)
	// Names vary by driver/locale: "Stereo Mix", "Wave Out Mix", "What U Hear"
	stereoMixNames := []string{"Stereo Mix", "Wave Out Mix", "What U Hear", "Lo que escucha"}
	for _, name := range stereoMixNames {
		if strings.Contains(stderr, name) {
			// Extract the full device name (may include driver info)
			fullName := extractDShowDeviceName(stderr, name)
			if fullName != "" {
				return &loopbackDevice{
					name:   "audio=" + fullName,
					format: "dshow",
				}, nil
			}
		}
	}

	// Priority 2: Look for VB-Audio Virtual Cable (more reliable, actively maintained)
	vbCableNames := []string{"CABLE Output", "VB-Audio Virtual Cable"}
	for _, name := range vbCableNames {
		if strings.Contains(stderr, name) {
			fullName := extractDShowDeviceName(stderr, name)
			if fullName != "" {
				return &loopbackDevice{
					name:   "audio=" + fullName,
					format: "dshow",
				}, nil
			}
		}
	}

	// Priority 3: Look for virtual-audio-capturer (legacy fallback)
	if strings.Contains(stderr, "virtual-audio-capturer") {
		return &loopbackDevice{
			name:   "audio=virtual-audio-capturer",
			format: "dshow",
		}, nil
	}

	return nil, &loopbackError{
		wrapped: ErrLoopbackNotFound,
		help:    loopbackInstallInstructionsWindows(),
	}
}

// extractDShowDeviceName extracts the full quoted device name from dshow output.
// Input like: [dshow @ 0x...] "Stereo Mix (Realtek High Definition Audio)"
// Returns: "Stereo Mix (Realtek High Definition Audio)"
func extractDShowDeviceName(stderr, partialName string) string {
	lines := strings.Split(stderr, "\n")
	for _, line := range lines {
		if strings.Contains(line, partialName) {
			// Extract quoted string
			start := strings.Index(line, "\"")
			if start == -1 {
				continue
			}
			end := strings.Index(line[start+1:], "\"")
			if end == -1 {
				continue
			}
			return line[start+1 : start+1+end]
		}
	}
	return ""
}

// --- Installation instructions per OS ---

func loopbackInstallInstructionsDarwin() string {
	return `BlackHole virtual audio driver not found.

To install BlackHole:
  brew install --cask blackhole-2ch

IMPORTANT - To hear audio while recording:
  BlackHole is a "black hole" - audio sent to it is NOT audible!
  You MUST create a Multi-Output Device to hear AND capture:

  1. Open "Audio MIDI Setup" (search in Spotlight)
  2. Click "+" > "Create Multi-Output Device"
  3. Check BOTH your speakers AND BlackHole 2ch
  4. Set this Multi-Output as your system output
  5. Use BlackHole 2ch as capture source (this tool detects it automatically)

For more info: https://github.com/ExistentialAudio/BlackHole`
}

func loopbackInstallInstructionsLinux() string {
	return `PulseAudio or PipeWire not detected.

Loopback capture requires PulseAudio or PipeWire (usually pre-installed).

NOTE: Linux loopback is SAFE - you will still hear audio normally.
      The monitor device is a passive copy of the output stream.

To install PulseAudio:
  Ubuntu/Debian: sudo apt install pulseaudio pulseaudio-utils
  Fedora:        sudo dnf install pulseaudio pulseaudio-utils
  Arch:          sudo pacman -S pulseaudio

To verify:
  pactl get-default-sink`
}

func loopbackInstallInstructionsWindows() string {
	return `No loopback audio device found.

Option 1 - Enable Stereo Mix (RECOMMENDED - no install, you keep hearing audio):
  1. Right-click speaker icon > Sound settings > More sound settings
  2. Recording tab > Right-click > Show Disabled Devices
  3. Enable "Stereo Mix" if present
  NOTE: Stereo Mix is SAFE - it's a passive copy, audio still plays normally.

Option 2 - Install VB-Audio Virtual Cable:
  1. Download from: https://vb-audio.com/Cable/
  2. Run installer as Administrator, reboot if prompted

  WARNING: VB-Cable does NOT relay audio to your speakers!
  To hear audio while recording, use VoiceMeeter instead:
    1. Download VoiceMeeter from: https://vb-audio.com/Voicemeeter/
    2. Route your audio through VoiceMeeter to BOTH speakers AND virtual output
    3. This tool will capture from the virtual output

For more info: https://vb-audio.com/Cable/`
}
