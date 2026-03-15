package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/alnah/transcript/internal/config"
	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/template"
)

// structureOptions holds validated options for the structure command.
type structureOptions struct {
	inputPath  string
	output     string
	template   template.Name
	outputLang lang.Language
	provider   Provider
}

// StructureCmd creates the structure command (restructure an existing transcript).
// The env parameter provides injectable dependencies for testing.
func StructureCmd(env *Env) *cobra.Command {
	var (
		output     string
		tmpl       string
		outputLang string
		provider   string
	)

	cmd := &cobra.Command{
		Use:   "structure <transcript-file>",
		Short: "Restructure an existing transcript",
		Long: `Restructure an existing transcript file using a template.

This command takes a raw transcript (typically generated without --template)
and restructures it into organized markdown using an LLM.

Restructuring uses DeepSeek by default, or OpenAI with --provider openai.`,
		Example: `  transcript structure meeting_raw.md -t meeting -o meeting.md
  transcript structure notes.md -t brainstorm
  transcript structure lecture.md -t lecture -T fr  # Translate to French
  transcript structure raw.md -t notes --provider openai`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Parse all inputs at the CLI boundary
			opts, err := parseStructureOptions(args[0], output, tmpl, outputLang, provider)
			if err != nil {
				return err
			}
			return runStructure(cmd, env, opts)
		},
	}

	cmd.Flags().StringVarP(&output, "output", "o", "", "Output file path (default: <input>_structured.md)")
	cmd.Flags().StringVarP(&tmpl, "template", "t", "", "Restructure template: brainstorm, meeting, lecture, notes (required)")
	cmd.Flags().StringVarP(&outputLang, "translate", "T", "", "Translate output to language (ISO 639-1 code, e.g., en, fr)")
	cmd.Flags().StringVar(&provider, "provider", ProviderDeepSeek, "LLM provider for restructuring: deepseek, openai")

	// Template is required for structure command.
	// Error is ignored: MarkFlagRequired only fails if flag doesn't exist,
	// which is a programming error caught at development time.
	_ = cmd.MarkFlagRequired("template")

	return cmd
}

// deriveStructuredOutputPath converts an input path to a structured output path.
// Example: "meeting.md" -> "meeting_structured.md"
func deriveStructuredOutputPath(inputPath string) string {
	ext := filepath.Ext(inputPath)
	base := strings.TrimSuffix(inputPath, ext)
	// Remove _raw suffix if present to avoid meeting_raw_structured.md
	base = strings.TrimSuffix(base, "_raw")
	return base + "_structured" + ext
}

// parseStructureOptions validates and parses CLI inputs into structureOptions.
// All parsing happens at the CLI boundary.
func parseStructureOptions(inputPath, output, tmpl, outputLang, provider string) (structureOptions, error) {
	// Parse template (required for structure command)
	parsedTemplate, err := template.ParseName(tmpl)
	if err != nil {
		return structureOptions{}, err
	}

	// Parse output language (optional)
	parsedOutputLang, err := lang.Parse(outputLang)
	if err != nil {
		return structureOptions{}, err
	}

	// Parse provider (optional, defaults handled in runStructure)
	var parsedProvider Provider
	if provider != "" {
		parsedProvider, err = ParseProvider(provider)
		if err != nil {
			return structureOptions{}, err
		}
	}

	return structureOptions{
		inputPath:  inputPath,
		output:     output,
		template:   parsedTemplate,
		outputLang: parsedOutputLang,
		provider:   parsedProvider,
	}, nil
}

// runStructure executes the structure command with validated options.
func runStructure(cmd *cobra.Command, env *Env, opts structureOptions) error {
	ctx := cmd.Context()

	// === VALIDATION (fail-fast) ===

	// 1. File exists
	if _, err := os.Stat(opts.inputPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("file not found: %s", opts.inputPath)
		}
		return fmt.Errorf("cannot access file: %w", err)
	}

	// 2. Load config for output-dir
	cfg, err := env.ConfigLoader.Load()
	if err != nil {
		fmt.Fprintf(env.Stderr, "Warning: failed to load config: %v\n", err)
	}

	// 3. Resolve output path (derive default from input basename only)
	// EnsureExtension adds .md only when path has no extension.
	// Paths with non-.md extensions are preserved and trigger a warning below.
	defaultOutput := deriveStructuredOutputPath(filepath.Base(opts.inputPath))
	output := config.ResolveOutputPath(opts.output, cfg.OutputDir, defaultOutput)
	output = config.EnsureExtension(output, ".md")
	warnNonMarkdownExtension(env.Stderr, output)

	// 4. Provider defaulting
	provider := opts.provider.OrDefault()

	// === READ INPUT ===

	fmt.Fprintf(env.Stderr, "Reading %s...\n", opts.inputPath)

	// #nosec G304 -- inputPath is user-provided, validated above
	content, err := os.ReadFile(opts.inputPath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	transcript := string(content)
	if strings.TrimSpace(transcript) == "" {
		return fmt.Errorf("input file is empty: %s", opts.inputPath)
	}

	// === RESTRUCTURE ===

	fmt.Fprintf(env.Stderr, "Restructuring with template '%s' (provider: %s)...\n", opts.template, provider)

	result, err := restructureContent(ctx, env, transcript, RestructureOptions{
		Template:   opts.template,
		Provider:   provider,
		OutputLang: opts.outputLang,
		OnProgress: func(phase string, current, total int) {
			if phase == "map" {
				fmt.Fprintf(env.Stderr, "  Processing part %d/%d...\n", current, total)
			} else {
				fmt.Fprintln(env.Stderr, "  Merging parts...")
			}
		},
	})
	if err != nil {
		return err
	}

	// === WRITE OUTPUT ===

	if err := writeFileAtomic(output, result); err != nil {
		return err
	}

	fmt.Fprintf(env.Stderr, "Done: %s\n", output)
	return nil
}
