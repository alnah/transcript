package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/config"
	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/template"
	"github.com/alnah/transcript/internal/transcribe"
)

// supportedFormats lists audio formats accepted by OpenAI's transcription API.
// Source: https://platform.openai.com/docs/guides/speech-to-text
var supportedFormats = map[string]bool{
	".ogg":  true,
	".mp3":  true,
	".wav":  true,
	".m4a":  true,
	".flac": true,
	".mp4":  true,
	".mpeg": true,
	".mpga": true,
	".webm": true,
}

// supportedFormatsList returns a sorted, comma-separated list for error messages.
// The list is sorted for deterministic output in tests and user-facing messages.
func supportedFormatsList() string {
	formats := make([]string, 0, len(supportedFormats))
	for ext := range supportedFormats {
		formats = append(formats, strings.TrimPrefix(ext, "."))
	}
	slices.Sort(formats)
	return strings.Join(formats, ", ")
}

// clampParallel constrains parallel request count to valid range [1, MaxRecommendedParallel].
func clampParallel(n int) int {
	if n < 1 {
		return 1
	}
	if n > transcribe.MaxRecommendedParallel {
		return transcribe.MaxRecommendedParallel
	}
	return n
}

// transcribeOptions holds validated options for the transcribe command.
type transcribeOptions struct {
	inputPath  string
	output     string
	template   template.Name
	diarize    bool
	parallel   int
	language   lang.Language
	outputLang lang.Language
	provider   Provider
}

// parseTranscribeOptions validates and parses CLI inputs into transcribeOptions.
// All parsing happens at the CLI boundary.
func parseTranscribeOptions(inputPath, output, tmpl string, diarize bool, parallel int, language, outputLang, provider string) (transcribeOptions, error) {
	// Parse template (optional for transcribe - empty means raw transcript)
	var parsedTemplate template.Name
	var err error
	if tmpl != "" {
		parsedTemplate, err = template.ParseName(tmpl)
		if err != nil {
			return transcribeOptions{}, err
		}
	}

	// Parse language flags
	parsedLanguage, err := lang.Parse(language)
	if err != nil {
		return transcribeOptions{}, err
	}
	parsedOutputLang, err := lang.Parse(outputLang)
	if err != nil {
		return transcribeOptions{}, err
	}

	// Parse provider (optional, defaults handled in runTranscribe)
	var parsedProvider Provider
	if provider != "" {
		parsedProvider, err = ParseProvider(provider)
		if err != nil {
			return transcribeOptions{}, err
		}
	}

	return transcribeOptions{
		inputPath:  inputPath,
		output:     output,
		template:   parsedTemplate,
		diarize:    diarize,
		parallel:   parallel,
		language:   parsedLanguage,
		outputLang: parsedOutputLang,
		provider:   parsedProvider,
	}, nil
}

// deriveOutputPath converts an audio file path to a markdown output path.
// Example: "session.ogg" -> "session.md"
func deriveOutputPath(inputPath string) string {
	ext := filepath.Ext(inputPath)
	return strings.TrimSuffix(inputPath, ext) + ".md"
}

// TranscribeCmd creates the transcribe command.
// The env parameter provides injectable dependencies for testing.
func TranscribeCmd(env *Env) *cobra.Command {
	var (
		output     string
		tmpl       string
		diarize    bool
		parallel   int
		language   string
		outputLang string
		provider   string
	)

	cmd := &cobra.Command{
		Use:   "transcribe <audio-file>",
		Short: "Transcribe an audio file",
		Long: `Transcribe an audio file using OpenAI's transcription API.

The audio is split into chunks at natural silence points, transcribed in parallel,
and optionally restructured using a template.

Transcription always uses OpenAI. Restructuring (--template) uses DeepSeek by default,
or OpenAI with --provider openai.

Supported formats: ogg, mp3, wav, m4a, flac, mp4, mpeg, mpga, webm`,
		Example: `  transcript transcribe session.ogg -o notes.md -t brainstorm
  transcript transcribe meeting.ogg -t meeting --diarize
  transcript transcribe lecture.ogg -t lecture -l en
  transcript transcribe session.ogg -l fr -T en -t meeting  # French audio, English output
  transcript transcribe session.ogg -t meeting --provider openai
  transcript transcribe session.ogg  # Raw transcript, no restructuring`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parse all inputs at the CLI boundary
			opts, err := parseTranscribeOptions(args[0], output, tmpl, diarize, parallel, language, outputLang, provider)
			if err != nil {
				return err
			}
			return runTranscribe(cmd, env, opts)
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (default: <input>.md)")
	cmd.Flags().StringVarP(&tmpl, "template", "t", "", "Restructure template: brainstorm, meeting, lecture, notes")
	cmd.Flags().BoolVar(&diarize, "diarize", false, "Enable speaker identification")
	cmd.Flags().IntVarP(&parallel, "parallel", "p", transcribe.MaxRecommendedParallel, "Max concurrent API requests (1-10)")
	cmd.Flags().StringVarP(&language, "language", "l", "", "Audio language (ISO 639-1 code, e.g., en, fr, pt-BR)")
	cmd.Flags().StringVarP(&outputLang, "translate", "T", "", "Translate output to language (ISO 639-1 code, requires --template)")
	cmd.Flags().StringVar(&provider, "provider", ProviderDeepSeek, "LLM provider for restructuring: deepseek, openai")

	return cmd
}

// runTranscribe executes the transcription pipeline with validated options.
func runTranscribe(cmd *cobra.Command, env *Env, opts transcribeOptions) error {
	ctx := cmd.Context()

	// === VALIDATION (fail-fast) ===

	// 1. File exists
	if _, err := os.Stat(opts.inputPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s", ErrFileNotFound, opts.inputPath)
		}
		return fmt.Errorf("cannot access input file: %w", err)
	}

	// 2. Format supported
	ext := strings.ToLower(filepath.Ext(opts.inputPath))
	if !supportedFormats[ext] {
		return fmt.Errorf("unsupported format %q (supported: %s): %w",
			ext, supportedFormatsList(), ErrUnsupportedFormat)
	}

	// 3. Load config for output-dir
	cfg, err := env.ConfigLoader.Load()
	if err != nil {
		fmt.Fprintf(env.Stderr, "Warning: failed to load config: %v\n", err)
	}

	// 4. Output path (resolve with output-dir, derive default from input if needed)
	// EnsureExtension adds .md only when path has no extension.
	// Paths with non-.md extensions are preserved and trigger a warning below.
	defaultOutput := deriveOutputPath(filepath.Base(opts.inputPath))
	output := config.ResolveOutputPath(opts.output, cfg.OutputDir, defaultOutput)
	output = config.EnsureExtension(output, ".md")
	warnNonMarkdownExtension(env.Stderr, output)

	// 5. Translate requires template
	if !opts.outputLang.IsZero() && opts.template.IsZero() {
		return fmt.Errorf("--translate requires --template (raw transcripts use the audio's language)")
	}

	// 6. Provider defaulting
	provider := opts.provider.OrDefault()

	// 7. Parallel bounds (clamp to 1-10)
	parallel := clampParallel(opts.parallel)

	// 8. API keys present (OpenAI always needed for transcription)
	openaiKey := env.Getenv(EnvOpenAIAPIKey)
	if openaiKey == "" {
		return fmt.Errorf("%w (set it with: export %s=sk-...)", ErrAPIKeyMissing, EnvOpenAIAPIKey)
	}

	// 9. Restructuring API key validation (only if template specified)
	// The actual key resolution is done in restructureContent()
	// Note: OpenAI key already validated above, so only check DeepSeek
	if !opts.template.IsZero() && provider.IsDeepSeek() {
		if env.Getenv(EnvDeepSeekAPIKey) == "" {
			return fmt.Errorf("%w (set it with: export %s=sk-...)", ErrDeepSeekKeyMissing, EnvDeepSeekAPIKey)
		}
	}

	// === SETUP ===

	// Resolve FFmpeg (may auto-download)
	ffmpegPath, err := env.FFmpegResolver.Resolve(ctx)
	if err != nil {
		return err
	}
	env.FFmpegResolver.CheckVersion(ctx, ffmpegPath)

	// === CHUNKING ===

	fmt.Fprintln(env.Stderr, "Detecting silences...")

	chunker, err := env.ChunkerFactory.NewSilenceChunker(ffmpegPath)
	if err != nil {
		return err
	}

	chunks, err := chunker.Chunk(ctx, opts.inputPath)
	if err != nil {
		return err
	}

	// Ensure cleanup even on error or interrupt
	defer func() {
		if cleanupErr := audio.CleanupChunks(chunks); cleanupErr != nil {
			fmt.Fprintf(env.Stderr, "Warning: failed to cleanup chunks: %v\n", cleanupErr)
		}
	}()

	fmt.Fprintf(env.Stderr, "Chunking audio... %d chunks\n", len(chunks))

	// === TRANSCRIPTION ===

	transcriber := env.TranscriberFactory.NewTranscriber(openaiKey)
	transcribeOpts := transcribe.Options{
		Diarize:  opts.diarize,
		Language: opts.language,
	}

	// Transcribe with progress output
	fmt.Fprintln(env.Stderr, "Transcribing...")
	results, err := transcribe.TranscribeAll(ctx, chunks, transcriber, transcribeOpts, parallel)
	if err != nil {
		return err
	}

	transcript := strings.Join(results, "\n\n")
	fmt.Fprintln(env.Stderr, "Transcription complete")

	// === RESTRUCTURE (optional) ===

	finalOutput := transcript
	if !opts.template.IsZero() && strings.TrimSpace(transcript) != "" {
		fmt.Fprintf(env.Stderr, "Restructuring with template '%s' (provider: %s)...\n", opts.template, provider)

		// Default output language to input language if not specified
		effectiveOutputLang := opts.outputLang
		if effectiveOutputLang.IsZero() && !opts.language.IsZero() {
			effectiveOutputLang = opts.language
		}

		finalOutput, err = restructureContent(ctx, env, transcript, RestructureOptions{
			Template:   opts.template,
			Provider:   provider,
			OutputLang: effectiveOutputLang,
			OnProgress: defaultProgressCallback(env.Stderr),
		})
		if err != nil {
			return err
		}
	}

	// === WRITE OUTPUT ===

	if err := writeFileAtomic(output, finalOutput); err != nil {
		return err
	}

	fmt.Fprintf(env.Stderr, "Done: %s\n", output)
	return nil
}
