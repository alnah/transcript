package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/config"
	"github.com/alnah/transcript/internal/format"
)

// recordOptions holds the validated options for the record command.
type recordOptions struct {
	duration     time.Duration
	output       string
	device       string
	systemRecord bool // Capture system audio instead of microphone (-s)
	mix          bool
}

// RecordCmd creates the record command.
// The env parameter provides injectable dependencies for testing.
func RecordCmd(env *Env) *cobra.Command {
	var (
		durationStr  string
		output       string
		device       string
		systemRecord bool
		mix          bool
	)

	cmd := &cobra.Command{
		Use:   "record",
		Short: "Record audio from microphone or system audio",
		Long: `Record audio from microphone, system audio (--system-record), or both mixed.

The output format is OGG Opus optimized for voice (~50kbps, 16kHz mono).
Recording can be interrupted with Ctrl+C to stop early - the file will be properly finalized.`,
		Example: `  transcript record -d 2h -o session.ogg           # Microphone only
  transcript record -d 30m -s                      # System audio only
  transcript record -d 1h --mix -o meeting.ogg     # Mic + system audio`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parse duration.
			duration, err := time.ParseDuration(durationStr)
			if err != nil {
				return fmt.Errorf("invalid duration %q: %w (use format like 2h, 30m, 1h30m)", durationStr, ErrInvalidDuration)
			}
			if duration <= 0 {
				return fmt.Errorf("duration must be positive: %w", ErrInvalidDuration)
			}

			// Note: output path resolution (including output-dir) is done in runRecord.
			opts := recordOptions{
				duration:     duration,
				output:       output,
				device:       device,
				systemRecord: systemRecord,
				mix:          mix,
			}

			return runRecord(cmd.Context(), env, opts)
		},
	}

	// Flags.
	cmd.Flags().StringVarP(&durationStr, "duration", "d", "", "Recording duration (e.g., 2h, 30m, 1h30m)")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (default: recording_<timestamp>.ogg)")
	cmd.Flags().StringVar(&device, "device", "", "Audio input device (default: system default)")
	cmd.Flags().BoolVarP(&systemRecord, "system-record", "s", false, "Capture system audio instead of microphone")
	cmd.Flags().BoolVar(&mix, "mix", false, "Capture both microphone and system audio")

	// Duration is required.
	_ = cmd.MarkFlagRequired("duration")

	// System-record and mix are mutually exclusive.
	cmd.MarkFlagsMutuallyExclusive("system-record", "mix")

	return cmd
}

// runRecord executes the recording with the given options.
func runRecord(ctx context.Context, env *Env, opts recordOptions) error {
	// Load config for output-dir.
	cfg, err := env.ConfigLoader.Load()
	if err != nil {
		fmt.Fprintf(env.Stderr, "Warning: failed to load config: %v\n", err)
	}

	// Resolve output path using config output-dir.
	opts.output = config.ResolveOutputPath(opts.output, cfg.OutputDir, defaultRecordingFilename(env.Now))

	// Add .ogg extension if output has no extension.
	if filepath.Ext(opts.output) == "" {
		opts.output += ".ogg"
	}

	// Warn if output extension is not .ogg.
	ext := strings.ToLower(filepath.Ext(opts.output))
	if ext != "" && ext != ".ogg" {
		fmt.Fprintf(env.Stderr, "Warning: output will be OGG Opus format regardless of %s extension\n", ext)
	}

	// Check output file doesn't already exist.
	if _, err := os.Stat(opts.output); err == nil {
		return fmt.Errorf("output file already exists: %s: %w", opts.output, ErrOutputExists)
	}

	// Resolve FFmpeg.
	ffmpegPath, err := env.FFmpegResolver.Resolve(ctx)
	if err != nil {
		return err
	}

	// Check FFmpeg version (warning only).
	env.FFmpegResolver.CheckVersion(ctx, ffmpegPath)

	// Create the appropriate recorder.
	recorder, err := createRecorder(ctx, env, ffmpegPath, opts.device, opts.systemRecord, opts.mix)
	if err != nil {
		return err
	}

	// Print start message.
	fmt.Fprintf(env.Stderr, "Recording for %s to %s... (press Ctrl+C to stop)\n", format.DurationHuman(opts.duration), opts.output)

	// Record.
	if err := recorder.Record(ctx, opts.duration, opts.output); err != nil {
		// Check if it was an interrupt - file may still be valid.
		if ctx.Err() != nil {
			fmt.Fprintln(env.Stderr, "Interrupted, finalizing...")
		} else {
			return err
		}
	}

	// Print completion message with file size.
	size, err := fileSize(opts.output)
	if err != nil {
		// File might not exist if recording failed early.
		return fmt.Errorf("recording failed: output file not created: %w", err)
	}

	fmt.Fprintf(env.Stderr, "Recording complete: %s (%s)\n", opts.output, format.Size(size))
	return nil
}

// createRecorder creates the appropriate recorder based on capture mode.
func createRecorder(ctx context.Context, env *Env, ffmpegPath, device string, systemRecord, mix bool) (audio.Recorder, error) {
	switch {
	case systemRecord:
		return env.RecorderFactory.NewLoopbackRecorder(ctx, ffmpegPath)
	case mix:
		return env.RecorderFactory.NewMixRecorder(ctx, ffmpegPath, device)
	default:
		return env.RecorderFactory.NewRecorder(ffmpegPath, device)
	}
}

// defaultRecordingFilename generates a default output filename with timestamp.
// Format: recording_20260125_143052.ogg
func defaultRecordingFilename(now func() time.Time) string {
	return fmt.Sprintf("recording_%s.ogg", now().Format("20060102_150405"))
}

// fileSize returns the size of a file in bytes.
func fileSize(path string) (int64, error) {
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}
