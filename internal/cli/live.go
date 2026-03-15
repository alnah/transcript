package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/config"
	"github.com/alnah/transcript/internal/format"
	"github.com/alnah/transcript/internal/interrupt"
	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/template"
	"github.com/alnah/transcript/internal/transcribe"
)

// postInterruptTimeout is the maximum time allowed for transcription and
// restructuring after recording is interrupted. This timeout uses a fresh
// context since the original context is cancelled by the interrupt.
const postInterruptTimeout = 30 * time.Minute

// LiveCmd creates the live command (record + transcribe in one step).
// The env parameter provides injectable dependencies for testing.
func LiveCmd(env *Env) *cobra.Command {
	var (
		durationStr       string
		output            string
		tmpl              string
		diarize           bool
		parallel          int
		keepAudio         bool
		keepRawTranscript bool
		keepAll           bool
		device            string
		systemRecord      bool
		mix               bool
		language          string
		translate         string
		provider          string
	)

	cmd := &cobra.Command{
		Use:   "live",
		Short: "Record and transcribe in one command",
		Long: `Record audio and transcribe it in a single operation.

This command combines 'record' and 'transcribe' for convenience.
The audio is recorded to a temporary file, transcribed, and optionally
restructured using a template. Use --keep-audio to preserve the recording.

Transcription always uses OpenAI. Restructuring (--template) uses DeepSeek by default,
or OpenAI with --provider openai.

Recording can be interrupted with Ctrl+C to stop early and continue transcription.
Press Ctrl+C twice within 2 seconds to abort entirely.`,
		Example: `  transcript live -d 2h -o ideas.md -t brainstorm
  transcript live -d 1h -t meeting --diarize -k       # Keep audio
  transcript live -d 1h -s -t meeting                 # System audio (video call)
  transcript live -d 1h --mix -t meeting              # Mic + system audio
  transcript live -d 1h -l fr -T en -t brainstorm     # French audio, English output
  transcript live -d 1h -t meeting -K                 # Keep audio and raw transcript`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parse duration.
			duration, err := time.ParseDuration(durationStr)
			if err != nil {
				return fmt.Errorf("invalid duration %q: %w (use format like 2h, 30m, 1h30m)", durationStr, ErrInvalidDuration)
			}
			if duration <= 0 {
				return fmt.Errorf("duration must be positive: %w", ErrInvalidDuration)
			}

			// Parse language flags at the boundary.
			parsedLanguage, err := lang.Parse(language)
			if err != nil {
				return err
			}
			parsedTranslate, err := lang.Parse(translate)
			if err != nil {
				return err
			}

			// Parse template at the boundary (empty string is allowed - means no restructuring).
			var parsedTemplate template.Name
			if tmpl != "" {
				parsedTemplate, err = template.ParseName(tmpl)
				if err != nil {
					return err
				}
			}

			// Parse provider at the boundary (empty string defaults to DeepSeek).
			var parsedProvider Provider
			if provider != "" {
				parsedProvider, err = ParseProvider(provider)
				if err != nil {
					return err
				}
			}

			// Note: output path resolution (including output-dir) is done in runLive.
			// --keep-all expands to --keep-audio + --keep-raw-transcript
			effectiveKeepAudio := keepAudio || keepAll
			effectiveKeepRaw := keepRawTranscript || keepAll

			return runLive(cmd.Context(), env, liveOptions{
				duration:          duration,
				output:            output,
				template:          parsedTemplate,
				diarize:           diarize,
				parallel:          parallel,
				keepAudio:         effectiveKeepAudio,
				keepRawTranscript: effectiveKeepRaw,
				device:            device,
				systemRecord:      systemRecord,
				mix:               mix,
				language:          parsedLanguage,
				translate:         parsedTranslate,
				provider:          parsedProvider,
			})
		},
	}

	// Recording flags.
	cmd.Flags().StringVarP(&durationStr, "duration", "d", "", "Recording duration (e.g., 2h, 30m, 1h30m)")
	cmd.Flags().StringVar(&device, "device", "", "Audio input device (default: system default)")
	cmd.Flags().BoolVarP(&systemRecord, "system-record", "s", false, "Capture system audio instead of microphone")
	cmd.Flags().BoolVar(&mix, "mix", false, "Capture both microphone and system audio")

	// Transcription flags.
	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (default: transcript_<timestamp>.md)")
	cmd.Flags().StringVarP(&tmpl, "template", "t", "", "Restructure template: brainstorm, meeting, lecture, notes")
	cmd.Flags().BoolVar(&diarize, "diarize", false, "Enable speaker identification")
	cmd.Flags().IntVarP(&parallel, "parallel", "p", transcribe.MaxRecommendedParallel, "Max concurrent API requests (1-10)")
	cmd.Flags().StringVarP(&language, "language", "l", "", "Audio language (ISO 639-1 code, e.g., en, fr, pt-BR)")
	cmd.Flags().StringVarP(&translate, "translate", "T", "", "Translate output to language (ISO 639-1 code, requires --template)")
	cmd.Flags().StringVar(&provider, "provider", ProviderDeepSeek, "LLM provider for restructuring: deepseek, openai")

	// Live-specific flags.
	cmd.Flags().BoolVarP(&keepAudio, "keep-audio", "k", false, "Keep the audio file after transcription")
	cmd.Flags().BoolVarP(&keepRawTranscript, "keep-raw-transcript", "r", false, "Keep raw transcript before restructuring (requires --template)")
	cmd.Flags().BoolVarP(&keepAll, "keep-all", "K", false, "Keep both audio and raw transcript (equivalent to -k -r)")

	// Duration is required.
	_ = cmd.MarkFlagRequired("duration")

	// System-record and mix are mutually exclusive.
	cmd.MarkFlagsMutuallyExclusive("system-record", "mix")

	return cmd
}

// liveOptions holds validated options for the live command.
type liveOptions struct {
	duration          time.Duration
	output            string // Markdown output path
	template          template.Name
	diarize           bool
	parallel          int
	keepAudio         bool
	keepRawTranscript bool // Keep raw transcript when using --template (-r)
	device            string
	systemRecord      bool // Capture system audio instead of microphone (-s)
	mix               bool
	language          lang.Language // Audio input language
	translate         lang.Language // Output language for restructuring (-T)
	provider          Provider      // LLM provider for restructuring
}

// audioOutputPath derives the audio file path from the markdown output path.
// Example: "notes.md" -> "notes.ogg"
func audioOutputPath(mdPath string) string {
	ext := filepath.Ext(mdPath)
	return strings.TrimSuffix(mdPath, ext) + ".ogg"
}

// rawTranscriptPath derives the raw transcript path from the final output path.
// Example: "notes.md" -> "notes_raw.md"
func rawTranscriptPath(mdPath string) string {
	ext := filepath.Ext(mdPath)
	return strings.TrimSuffix(mdPath, ext) + "_raw" + ext
}

// defaultLiveFilename generates a default output filename with timestamp.
// Format: transcript_20260125_143052.md
func defaultLiveFilename(now func() time.Time) string {
	return fmt.Sprintf("transcript_%s.md", now().Format("20060102_150405"))
}

// liveContext holds validated context for live command execution.
// This is separate from cli.Env to hold command-specific resolved values.
type liveContext struct {
	openaiKey           string   // OpenAI API key (always needed for transcription)
	restructureAPIKey   string   // API key for restructuring (depends on provider)
	restructureProvider Provider // LLM provider for restructuring
	ffmpegPath          string
	audioPath           string // Final audio path (if --keep-audio / -k)
	rawTranscriptPath   string // Path for raw transcript (if --keep-raw-transcript / -r)
	parallel            int
}

// validateLiveContext performs fail-fast validation before any I/O.
func validateLiveContext(ctx context.Context, env *Env, opts liveOptions) (*liveContext, error) {
	// 1. Provider defaulting (validation done at parse time in RunE)
	provider := opts.provider.OrDefault()

	// 2. OpenAI API key present (always needed for transcription)
	openaiKey := env.Getenv(EnvOpenAIAPIKey)
	if openaiKey == "" {
		return nil, fmt.Errorf("%w (set it with: export %s=sk-...)", ErrAPIKeyMissing, EnvOpenAIAPIKey)
	}

	// 3. Restructuring API key (only if template specified)
	var restructureAPIKey string
	if !opts.template.IsZero() {
		switch {
		case provider.IsDeepSeek():
			restructureAPIKey = env.Getenv(EnvDeepSeekAPIKey)
			if restructureAPIKey == "" {
				return nil, fmt.Errorf("%w (set it with: export %s=sk-...)", ErrDeepSeekKeyMissing, EnvDeepSeekAPIKey)
			}
		case provider.IsOpenAI():
			restructureAPIKey = openaiKey // Reuse OpenAI key
		}
	}

	// 4. FFmpeg available (may auto-download)
	ffmpegPath, err := env.FFmpegResolver.Resolve(ctx)
	if err != nil {
		return nil, err
	}
	env.FFmpegResolver.CheckVersion(ctx, ffmpegPath)

	// 5. Template validation: already done at parse time (template.ParseName in RunE)

	// 6. Language validation: already done at parse time (lang.Parse in RunE)

	// 7. Translate requires template
	if !opts.translate.IsZero() && opts.template.IsZero() {
		return nil, fmt.Errorf("--translate requires --template (raw transcripts use the audio's language)")
	}

	// 8. Keep raw transcript requires template
	if opts.keepRawTranscript && opts.template.IsZero() {
		return nil, fmt.Errorf("--keep-raw-transcript requires --template (without template, output is already the raw transcript)")
	}

	// 9. Output file doesn't exist
	if _, err := os.Stat(opts.output); err == nil {
		return nil, fmt.Errorf("output file already exists: %s: %w", opts.output, ErrOutputExists)
	}

	// 10. Audio output path doesn't exist (if --keep-audio)
	audioPath := audioOutputPath(opts.output)
	if opts.keepAudio {
		if _, err := os.Stat(audioPath); err == nil {
			return nil, fmt.Errorf("audio file already exists: %s: %w", audioPath, ErrOutputExists)
		}
	}

	// 11. Raw transcript path doesn't exist (if --keep-raw-transcript)
	rawPath := rawTranscriptPath(opts.output)
	if opts.keepRawTranscript {
		if _, err := os.Stat(rawPath); err == nil {
			return nil, fmt.Errorf("raw transcript file already exists: %s: %w", rawPath, ErrOutputExists)
		}
	}

	// 12. System audio device available (if needed)
	if opts.systemRecord || opts.mix {
		if _, err := audio.DetectLoopbackDevice(ctx, ffmpegPath); err != nil {
			return nil, err
		}
	}

	return &liveContext{
		openaiKey:           openaiKey,
		restructureAPIKey:   restructureAPIKey,
		restructureProvider: provider,
		ffmpegPath:          ffmpegPath,
		audioPath:           audioPath,
		rawTranscriptPath:   rawPath,
		parallel:            clampParallel(opts.parallel),
	}, nil
}

// liveRecordResult holds the result of the recording phase.
type liveRecordResult struct {
	audioPath      string // Path to the recorded audio
	tempDir        string // Temp directory to cleanup (empty if --keep-audio moved the file)
	cleanupTempDir bool   // Whether to cleanup tempDir on exit
}

// liveRecordPhase executes the recording phase.
func liveRecordPhase(ctx context.Context, env *Env, lctx *liveContext, opts liveOptions) (*liveRecordResult, error) {
	// Create temporary file for recording
	tempDir, err := os.MkdirTemp("", "transcript-live-*")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp directory: %w", err)
	}
	tempAudioPath := filepath.Join(tempDir, "recording.ogg")

	result := &liveRecordResult{
		audioPath:      tempAudioPath,
		tempDir:        tempDir,
		cleanupTempDir: true,
	}

	// Create recorder
	recorder, err := createRecorder(ctx, env, lctx.ffmpegPath, opts.device, opts.systemRecord, opts.mix)
	if err != nil {
		return result, err
	}

	fmt.Fprintf(env.Stderr, "Recording for %s... (press Ctrl+C to stop early)\n", format.DurationHuman(opts.duration))

	// Record to temp file
	recordErr := recorder.Record(ctx, opts.duration, tempAudioPath)

	// Check for interrupt during recording
	if ctx.Err() != nil {
		if size, statErr := fileSize(tempAudioPath); statErr == nil && size > 0 {
			fmt.Fprintf(env.Stderr, "\nRecording interrupted. Partial audio saved to: %s (%s)\n",
				tempAudioPath, format.Size(size))
			result.cleanupTempDir = false // Keep temp dir for recovery
		}
		return result, ctx.Err()
	}

	if recordErr != nil {
		return result, recordErr
	}

	// Verify recording produced non-empty file
	audioSize, err := fileSize(tempAudioPath)
	if err != nil {
		return result, fmt.Errorf("recording failed: output file not created")
	}
	if audioSize == 0 {
		return result, fmt.Errorf("recording produced empty file (check your audio device)")
	}

	fmt.Fprintf(env.Stderr, "Recording complete: %s\n", format.Size(audioSize))

	// Move audio to final location if --keep-audio
	if opts.keepAudio {
		if err := moveFile(tempAudioPath, lctx.audioPath); err != nil {
			return result, fmt.Errorf("failed to save audio file: %w", err)
		}
		result.audioPath = lctx.audioPath
		fmt.Fprintf(env.Stderr, "Audio saved: %s\n", lctx.audioPath)
	}

	return result, nil
}

// liveTranscribePhase executes chunking and transcription.
func liveTranscribePhase(ctx context.Context, env *Env, lctx *liveContext, opts liveOptions, audioPath string) (string, error) {
	fmt.Fprintln(env.Stderr, "Detecting silences...")

	chunker, err := env.ChunkerFactory.NewSilenceChunker(lctx.ffmpegPath)
	if err != nil {
		return "", err
	}

	chunks, err := chunker.Chunk(ctx, audioPath)
	if err != nil {
		return "", err
	}
	defer func() {
		if cleanupErr := audio.CleanupChunks(chunks); cleanupErr != nil {
			fmt.Fprintf(env.Stderr, "Warning: failed to cleanup chunks: %v\n", cleanupErr)
		}
	}()

	fmt.Fprintf(env.Stderr, "Chunking audio... %d chunks\n", len(chunks))

	transcriber := env.TranscriberFactory.NewTranscriber(lctx.openaiKey)
	transcribeOpts := transcribe.Options{
		Diarize:  opts.diarize,
		Language: opts.language,
	}

	fmt.Fprintln(env.Stderr, "Transcribing...")

	results, err := transcribe.TranscribeAll(ctx, chunks, transcriber, transcribeOpts, lctx.parallel)
	if err != nil {
		if opts.keepAudio {
			fmt.Fprintf(env.Stderr, "\nTranscription failed. Audio is available at: %s\n", audioPath)
		}
		return "", err
	}

	fmt.Fprintln(env.Stderr, "Transcription complete")
	return strings.Join(results, "\n\n"), nil
}

// liveRestructurePhase optionally restructures the transcript.
// If opts.keepRawTranscript is true, saves the raw transcript before restructuring.
func liveRestructurePhase(ctx context.Context, env *Env, lctx *liveContext, opts liveOptions, transcript, audioPath string) (string, error) {
	if opts.template.IsZero() {
		return transcript, nil
	}

	// Save raw transcript if requested (before restructuring, so it's available on failure)
	if opts.keepRawTranscript {
		if err := writeRawTranscript(env, lctx.rawTranscriptPath, transcript); err != nil {
			return "", err
		}
	}

	fmt.Fprintf(env.Stderr, "Restructuring with template '%s' (provider: %s)...\n", opts.template, lctx.restructureProvider)

	// Default output language to input language if not specified
	effectiveOutputLang := opts.translate
	if effectiveOutputLang.IsZero() && !opts.language.IsZero() {
		effectiveOutputLang = opts.language
	}

	result, err := restructureContent(ctx, env, transcript, RestructureOptions{
		Template:   opts.template,
		Provider:   lctx.restructureProvider,
		OutputLang: effectiveOutputLang,
		OnProgress: defaultProgressCallback(env.Stderr),
	})
	if err != nil {
		if opts.keepAudio {
			fmt.Fprintf(env.Stderr, "\nRestructuring failed. Audio is available at: %s\n", audioPath)
		}
		if opts.keepRawTranscript {
			fmt.Fprintf(env.Stderr, "Raw transcript is available at: %s\n", lctx.rawTranscriptPath)
		}
		return "", err
	}

	return result, nil
}

// writeRawTranscript saves the raw transcript to a file.
func writeRawTranscript(env *Env, path, content string) error {
	// #nosec G302 G304 -- user-specified output file with standard permissions
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("raw transcript file already exists: %s: %w", path, ErrOutputExists)
		}
		return fmt.Errorf("cannot create raw transcript file: %w", err)
	}

	writeErr := func() error {
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(content); err != nil {
			return fmt.Errorf("failed to write raw transcript: %w", err)
		}
		return nil
	}()

	if writeErr != nil {
		_ = os.Remove(path)
		return writeErr
	}

	fmt.Fprintf(env.Stderr, "Raw transcript saved: %s\n", path)
	return nil
}

// liveWritePhase writes the final output atomically.
func liveWritePhase(env *Env, output, content string) error {
	if err := writeFileAtomic(output, content); err != nil {
		return err
	}
	fmt.Fprintf(env.Stderr, "Done: %s\n", output)
	return nil
}

// runLive executes the live recording and transcription pipeline.
// Supports graceful interrupt: first Ctrl+C stops recording and continues transcription,
// second Ctrl+C within 2s aborts entirely.
func runLive(parentCtx context.Context, env *Env, opts liveOptions) error {
	// Load config for output-dir.
	cfg, err := env.ConfigLoader.Load()
	if err != nil {
		fmt.Fprintf(env.Stderr, "Warning: failed to load config: %v\n", err)
	}

	// Resolve output path using config output-dir.
	// EnsureExtension adds .md only when path has no extension.
	// Paths with non-.md extensions are preserved and trigger a warning below.
	opts.output = config.ResolveOutputPath(opts.output, cfg.OutputDir, defaultLiveFilename(env.Now))
	opts.output = config.EnsureExtension(opts.output, ".md")
	warnNonMarkdownExtension(env.Stderr, opts.output)

	// Set up interrupt handler for double Ctrl+C detection.
	interruptHandler, ctx := interrupt.NewHandler(parentCtx)
	defer interruptHandler.Stop()

	// Validate environment (fail-fast)
	lctx, err := validateLiveContext(ctx, env, opts)
	if err != nil {
		return err
	}

	// Recording phase
	recordResult, recordErr := liveRecordPhase(ctx, env, lctx, opts)

	// Set up cleanup for temp directory
	if recordResult != nil && recordResult.cleanupTempDir && recordResult.tempDir != "" {
		defer func() { _ = os.RemoveAll(recordResult.tempDir) }()
	}

	// Handle recording interruption
	if recordErr != nil {
		return handleRecordingInterrupt(env, interruptHandler, recordResult, recordErr, lctx, opts)
	}

	// Normal flow: recording completed successfully
	return runLiveTranscriptionPipeline(ctx, env, lctx, opts, recordResult.audioPath)
}

// handleRecordingInterrupt handles the case where recording was interrupted.
// If a valid partial recording exists, it asks the user whether to continue
// with transcription or abort. Returns the original recordErr if not a recoverable
// interrupt, or runs transcription on partial recording.
func handleRecordingInterrupt(env *Env, handler *interrupt.Handler, result *liveRecordResult, recordErr error, lctx *liveContext, opts liveOptions) error {
	// Check if this was an interrupt with valid partial recording
	if !handler.WasInterrupted() || result == nil || result.audioPath == "" {
		return recordErr
	}

	size, statErr := fileSize(result.audioPath)
	if statErr != nil || size == 0 {
		return recordErr
	}

	// Ask user intent via timeout window
	behavior := handler.WaitForDecision(
		"Ctrl+C again to discard, wait 2s to transcribe partial recording...")

	if behavior == interrupt.Abort {
		return context.Canceled
	}

	// Continue with transcription of partial recording
	fmt.Fprintf(env.Stderr, "\nContinuing with partial recording (%s)...\n", format.Size(size))

	// Move audio to final location if --keep-audio
	if opts.keepAudio {
		if moveErr := moveFile(result.audioPath, lctx.audioPath); moveErr != nil {
			fmt.Fprintf(env.Stderr, "Warning: failed to save audio: %v\n", moveErr)
		} else {
			result.audioPath = lctx.audioPath
			fmt.Fprintf(env.Stderr, "Audio saved: %s\n", lctx.audioPath)
		}
	}

	// Create fresh context for transcription (original is cancelled by interrupt).
	// We use context.Background() because the parent context is already done.
	transcribeCtx, cancel := context.WithTimeout(context.Background(), postInterruptTimeout)
	defer cancel()

	return runLiveTranscriptionPipeline(transcribeCtx, env, lctx, opts, result.audioPath)
}

// runLiveTranscriptionPipeline runs the transcription and restructuring phases.
func runLiveTranscriptionPipeline(ctx context.Context, env *Env, lctx *liveContext, opts liveOptions, audioPath string) error {
	// Transcription phase
	transcript, err := liveTranscribePhase(ctx, env, lctx, opts, audioPath)
	if err != nil {
		return err
	}

	// Restructure phase (optional)
	finalOutput, err := liveRestructurePhase(ctx, env, lctx, opts, transcript, audioPath)
	if err != nil {
		return err
	}

	// Write output
	return liveWritePhase(env, opts.output, finalOutput)
}

// moveFile moves a file from src to dst.
// Uses os.Rename if possible (same filesystem), otherwise copies and removes.
func moveFile(src, dst string) error {
	// Try rename first (fast, atomic if same filesystem)
	if err := os.Rename(src, dst); err == nil {
		return nil
	}

	// Rename failed (probably cross-filesystem), fall back to copy
	return copyFile(src, dst)
}

// copyFile copies a file from src to dst, then removes src.
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src) // #nosec G304 -- src is internal temp file
	if err != nil {
		return err
	}
	defer func() { _ = srcFile.Close() }()

	// Get source file info for permissions
	srcInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}

	dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, srcInfo.Mode()) // #nosec G304 -- dst is user-specified output
	if err != nil {
		return err
	}

	_, copyErr := io.Copy(dstFile, srcFile)
	closeErr := dstFile.Close()

	if copyErr != nil {
		_ = os.Remove(dst) // Clean up partial file
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dst)
		return closeErr
	}

	// Remove source file after successful copy
	return os.Remove(src)
}
