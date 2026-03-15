package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"

	"github.com/alnah/transcript/internal/apierr"
	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/cli"
	"github.com/alnah/transcript/internal/ffmpeg"
	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/restructure"
	"github.com/alnah/transcript/internal/template"
)

// Injected at build time via ldflags.
var (
	version = "dev"
	commit  = "unknown"
)

// Exit codes per specification.
const (
	ExitOK            = 0
	ExitGeneral       = 1
	ExitUsage         = 2
	ExitSetup         = 3
	ExitValidation    = 4
	ExitTranscription = 5
	ExitRestructure   = 6
	ExitInterrupt     = 130
)

func main() {
	// Load .env file if present (ignore error if missing).
	_ = godotenv.Load()

	// Context with signal cancellation.
	ctx, cancel := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Create the CLI environment with production defaults.
	env := cli.DefaultEnv()

	// Root command.
	rootCmd := &cobra.Command{
		Use:     "transcript",
		Short:   "Record, transcribe, and restructure audio sessions",
		Version: fmt.Sprintf("%s (commit: %s)", version, commit),
		// Silence Cobra's default error/usage printing; we handle it ourselves.
		SilenceErrors: true,
		SilenceUsage:  true,
	}

	// Subcommands.
	rootCmd.AddCommand(cli.RecordCmd(env))
	rootCmd.AddCommand(cli.TranscribeCmd(env))
	rootCmd.AddCommand(cli.LiveCmd(env))
	rootCmd.AddCommand(cli.StructureCmd(env))
	rootCmd.AddCommand(cli.ConfigCmd(env))
	rootCmd.AddCommand(cli.DevicesCmd(env))

	if err := rootCmd.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

// exitCode maps errors to spec-defined exit codes.
func exitCode(err error) int {
	if err == nil {
		return ExitOK
	}

	// Check for context cancellation (interrupt).
	if errors.Is(err, context.Canceled) {
		return ExitInterrupt
	}

	// Usage errors (ExitUsage = 2): Cobra flag/arg parsing errors.
	// Cobra doesn't expose typed errors, so we check for known error message patterns.
	// These patterns are stable across Cobra versions (tested with v1.8+).
	if isCobraUsageError(err) {
		return ExitUsage
	}

	// Setup errors (ExitSetup = 3).
	if errors.Is(err, ffmpeg.ErrNotFound) || errors.Is(err, cli.ErrAPIKeyMissing) ||
		errors.Is(err, cli.ErrDeepSeekKeyMissing) || errors.Is(err, cli.ErrUnsupportedProvider) ||
		errors.Is(err, audio.ErrNoAudioDevice) || errors.Is(err, audio.ErrLoopbackNotFound) ||
		errors.Is(err, ffmpeg.ErrUnsupportedPlatform) || errors.Is(err, ffmpeg.ErrChecksumMismatch) ||
		errors.Is(err, ffmpeg.ErrDownloadFailed) {
		return ExitSetup
	}

	// Validation errors (ExitValidation = 4).
	if errors.Is(err, cli.ErrInvalidDuration) || errors.Is(err, cli.ErrUnsupportedFormat) ||
		errors.Is(err, cli.ErrFileNotFound) || errors.Is(err, template.ErrUnknown) ||
		errors.Is(err, cli.ErrOutputExists) || errors.Is(err, audio.ErrChunkingFailed) ||
		errors.Is(err, audio.ErrChunkTooLarge) || errors.Is(err, lang.ErrInvalid) {
		return ExitValidation
	}

	// Transcription errors (ExitTranscription = 5).
	if errors.Is(err, apierr.ErrRateLimit) || errors.Is(err, apierr.ErrQuotaExceeded) ||
		errors.Is(err, apierr.ErrTimeout) || errors.Is(err, apierr.ErrAuthFailed) {
		return ExitTranscription
	}

	// Restructure errors (ExitRestructure = 6).
	if errors.Is(err, restructure.ErrTranscriptTooLong) {
		return ExitRestructure
	}

	return ExitGeneral
}

// cobraUsageErrorPatterns contains error message substrings that indicate Cobra usage errors.
// These patterns are stable across Cobra versions (tested with v1.8+).
// Cobra doesn't expose typed errors, so string matching is the only reliable approach.
var cobraUsageErrorPatterns = []string{
	"required flag",             // Missing required flag
	"unknown flag",              // Flag doesn't exist
	"unknown shorthand",         // Short flag doesn't exist
	"flag needs an argument",    // Flag provided without value
	"invalid argument",          // Invalid flag value type
	"if any flags in the group", // Mutually exclusive flag violation
	"accepts ",                  // Wrong number of arguments (e.g., "accepts 1 arg(s)")
	"requires at least",         // Too few arguments
	"requires at most",          // Too many arguments
}

// isCobraUsageError checks if an error is a Cobra usage/parsing error.
func isCobraUsageError(err error) bool {
	if err == nil {
		return false
	}
	errMsg := err.Error()
	for _, pattern := range cobraUsageErrorPatterns {
		if strings.Contains(errMsg, pattern) {
			return true
		}
	}
	return false
}
