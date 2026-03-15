package cli

import (
	"context"
	"fmt"

	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/restructure"
	"github.com/alnah/transcript/internal/template"
)

// RestructureOptions configures transcript restructuring.
type RestructureOptions struct {
	// Template (required): validated template name
	Template template.Name
	// Provider (required): validated LLM provider
	Provider Provider
	// Output language (optional): zero value = English (template's native language)
	OutputLang lang.Language
	// Optional progress callback for long transcripts
	OnProgress func(phase string, current, total int)
}

// restructureContent transforms content using a template and LLM.
// Resolves API key internally based on opts.Provider.
// Template and Provider must be validated before calling this function.
func restructureContent(ctx context.Context, env *Env, content string, opts RestructureOptions) (string, error) {
	// 1. Default provider to DeepSeek if not specified
	opts.Provider = opts.Provider.OrDefault()

	// 2. Resolve API key based on provider
	var apiKey string
	if opts.Provider.IsDeepSeek() {
		apiKey = env.Getenv(EnvDeepSeekAPIKey)
		if apiKey == "" {
			return "", fmt.Errorf("%w (set it with: export %s=sk-...)", ErrDeepSeekKeyMissing, EnvDeepSeekAPIKey)
		}
	} else if opts.Provider.IsOpenAI() {
		apiKey = env.Getenv(EnvOpenAIAPIKey)
		if apiKey == "" {
			return "", fmt.Errorf("%w (set it with: export %s=sk-...)", ErrAPIKeyMissing, EnvOpenAIAPIKey)
		}
	}
	// Note: invalid provider case is now impossible since Provider type guarantees validity

	// 3. Create restructurer with options
	var mrOpts []restructure.MapReduceOption
	if opts.OnProgress != nil {
		mrOpts = append(mrOpts, restructure.WithMapReduceProgress(opts.OnProgress))
	}

	mr, err := env.RestructurerFactory.NewMapReducer(opts.Provider, apiKey, mrOpts...)
	if err != nil {
		return "", err
	}

	// 4. Restructure content
	result, _, err := mr.Restructure(ctx, content, opts.Template, opts.OutputLang)
	return result, err
}
