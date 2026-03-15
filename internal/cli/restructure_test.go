package cli

import (
	"context"
	"errors"
	"testing"

	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/restructure"
	"github.com/alnah/transcript/internal/template"
)

// Notes:
// - Tests focus on restructureContent which is the shared restructuring logic
// - Provider defaulting, API key validation, and template validation are tested
// - The actual restructuring is mocked via mockRestructurerFactory
// - Progress callback is tested via mock inspection

// ---------------------------------------------------------------------------
// Tests for restructureContent - Shared restructuring logic
// ---------------------------------------------------------------------------

func TestRestructureContent_DefaultProvider(t *testing.T) {
	t.Parallel()

	mockMR := &mockMapReduceRestructurer{
		RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
			return "restructured", false, nil
		},
	}
	restructurerFactory := &mockRestructurerFactory{
		mockMapReducer: mockMR,
	}

	env := &Env{
		Stderr:              &syncBuffer{},
		Getenv:              defaultTestEnv,
		RestructurerFactory: restructurerFactory,
	}

	// Zero provider should default to DeepSeek
	_, err := RestructureContent(context.Background(), env, "content", RestructureOptions{
		Template: template.MustParseName("brainstorm"),
		// Provider omitted - zero value should default to deepseek
	})
	if err != nil {
		t.Fatalf("RestructureContent() unexpected error: %v", err)
	}

	calls := restructurerFactory.NewMapReducerCalls()
	if len(calls) != 1 {
		t.Fatalf("NewMapReducer() calls = %d, want 1", len(calls))
	}
	if calls[0].Provider != DeepSeekProvider {
		t.Errorf("NewMapReducer() provider = %q, want %q", calls[0].Provider, DeepSeekProvider)
	}
}

func TestRestructureContent_DeepSeekMissingKey(t *testing.T) {
	t.Parallel()

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: func(key string) string {
			if key == EnvOpenAIAPIKey {
				return "openai-key"
			}
			return "" // No DeepSeek key
		},
		RestructurerFactory: &mockRestructurerFactory{},
	}

	_, err := RestructureContent(context.Background(), env, "content", RestructureOptions{
		Template: template.MustParseName("brainstorm"),
		Provider: DeepSeekProvider,
	})

	if err == nil {
		t.Fatal("RestructureContent() error = nil, want ErrDeepSeekKeyMissing")
	}
	if !errors.Is(err, ErrDeepSeekKeyMissing) {
		t.Errorf("RestructureContent() error = %v, want ErrDeepSeekKeyMissing", err)
	}
}

func TestRestructureContent_OpenAIMissingKey(t *testing.T) {
	t.Parallel()

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: func(key string) string {
			if key == EnvDeepSeekAPIKey {
				return "deepseek-key"
			}
			return "" // No OpenAI key
		},
		RestructurerFactory: &mockRestructurerFactory{},
	}

	_, err := RestructureContent(context.Background(), env, "content", RestructureOptions{
		Template: template.MustParseName("brainstorm"),
		Provider: OpenAIProvider,
	})

	if err == nil {
		t.Fatal("RestructureContent() error = nil, want ErrAPIKeyMissing")
	}
	if !errors.Is(err, ErrAPIKeyMissing) {
		t.Errorf("RestructureContent() error = %v, want ErrAPIKeyMissing", err)
	}
}

func TestRestructureContent_FactoryError(t *testing.T) {
	t.Parallel()

	factoryErr := errors.New("factory initialization failed")
	restructurerFactory := &mockRestructurerFactory{
		NewMapReducerErr: factoryErr,
	}

	env := &Env{
		Stderr:              &syncBuffer{},
		Getenv:              defaultTestEnv,
		RestructurerFactory: restructurerFactory,
	}

	_, err := RestructureContent(context.Background(), env, "content", RestructureOptions{
		Template: template.MustParseName("brainstorm"),
		Provider: DeepSeekProvider,
	})

	if err == nil {
		t.Fatal("RestructureContent() error = nil, want factory error")
	}
	if !errors.Is(err, factoryErr) {
		t.Errorf("RestructureContent() error = %v, want factory error", err)
	}
}

func TestRestructureContent_Success(t *testing.T) {
	t.Parallel()

	mockMR := &mockMapReduceRestructurer{
		RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
			return "# Restructured\n\nContent here.", false, nil
		},
	}
	restructurerFactory := &mockRestructurerFactory{
		mockMapReducer: mockMR,
	}

	env := &Env{
		Stderr:              &syncBuffer{},
		Getenv:              defaultTestEnv,
		RestructurerFactory: restructurerFactory,
	}

	result, err := RestructureContent(context.Background(), env, "raw content", RestructureOptions{
		Template: template.MustParseName("brainstorm"),
		Provider: DeepSeekProvider,
	})

	if err != nil {
		t.Fatalf("RestructureContent() unexpected error: %v", err)
	}
	if result != "# Restructured\n\nContent here." {
		t.Errorf("RestructureContent() = %q, want %q", result, "# Restructured\n\nContent here.")
	}

	// Verify the restructurer was called with correct args
	calls := mockMR.RestructureCalls()
	if len(calls) != 1 {
		t.Fatalf("Restructure() calls = %d, want 1", len(calls))
	}
	if calls[0].Transcript != "raw content" {
		t.Errorf("Restructure() transcript = %q, want %q", calls[0].Transcript, "raw content")
	}
	if calls[0].TemplateName.String() != "brainstorm" {
		t.Errorf("Restructure() template = %q, want %q", calls[0].TemplateName, "brainstorm")
	}
}

func TestRestructureContent_WithOutputLang(t *testing.T) {
	t.Parallel()

	var capturedLang lang.Language
	mockMR := &mockMapReduceRestructurer{
		RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
			capturedLang = outputLang
			return "restructured", false, nil
		},
	}
	restructurerFactory := &mockRestructurerFactory{
		mockMapReducer: mockMR,
	}

	env := &Env{
		Stderr:              &syncBuffer{},
		Getenv:              defaultTestEnv,
		RestructurerFactory: restructurerFactory,
	}

	_, err := RestructureContent(context.Background(), env, "content", RestructureOptions{
		Template:   template.MustParseName("meeting"),
		Provider:   DeepSeekProvider,
		OutputLang: lang.MustParse("fr"),
	})

	if err != nil {
		t.Fatalf("RestructureContent() unexpected error: %v", err)
	}
	if capturedLang.String() != "fr" {
		t.Errorf("Restructure() outputLang = %q, want %q", capturedLang.String(), "fr")
	}
}

func TestRestructureContent_WithProgressCallback(t *testing.T) {
	t.Parallel()

	mockMR := &mockMapReduceRestructurer{
		RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
			return "restructured", false, nil
		},
	}

	var capturedOpts []restructure.MapReduceOption
	restructurerFactory := &mockRestructurerFactory{
		NewMapReducerFunc: func(provider Provider, apiKey string, opts ...restructure.MapReduceOption) (restructure.MapReducer, error) {
			capturedOpts = opts
			return mockMR, nil
		},
	}

	env := &Env{
		Stderr:              &syncBuffer{},
		Getenv:              defaultTestEnv,
		RestructurerFactory: restructurerFactory,
	}

	_, err := RestructureContent(context.Background(), env, "content", RestructureOptions{
		Template: template.MustParseName("brainstorm"),
		Provider: DeepSeekProvider,
		OnProgress: func(phase string, current, total int) {
			// Callback provided to verify option is passed to factory
		},
	})

	if err != nil {
		t.Fatalf("RestructureContent() unexpected error: %v", err)
	}

	// Verify that options were passed to the factory.
	// Note: We only verify the option is passed, not that it's invoked,
	// since the mock doesn't call the callback.
	if len(capturedOpts) == 0 {
		t.Error("NewMapReducer() options = 0, want > 0")
	}
}

func TestRestructureContent_RestructureError(t *testing.T) {
	t.Parallel()

	restructureErr := errors.New("LLM API error")
	mockMR := &mockMapReduceRestructurer{
		RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
			return "", false, restructureErr
		},
	}
	restructurerFactory := &mockRestructurerFactory{
		mockMapReducer: mockMR,
	}

	env := &Env{
		Stderr:              &syncBuffer{},
		Getenv:              defaultTestEnv,
		RestructurerFactory: restructurerFactory,
	}

	_, err := RestructureContent(context.Background(), env, "content", RestructureOptions{
		Template: template.MustParseName("brainstorm"),
		Provider: DeepSeekProvider,
	})

	if err == nil {
		t.Fatal("RestructureContent() error = nil, want restructure error")
	}
	if !errors.Is(err, restructureErr) {
		t.Errorf("RestructureContent() error = %v, want restructure error", err)
	}
}

func TestRestructureContent_CorrectAPIKeyUsed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		provider    Provider
		expectedKey string
	}{
		{"deepseek uses deepseek key", DeepSeekProvider, "test-deepseek-key"},
		{"openai uses openai key", OpenAIProvider, "test-openai-key"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mockMR := &mockMapReduceRestructurer{
				RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
					return "restructured", false, nil
				},
			}
			restructurerFactory := &mockRestructurerFactory{
				mockMapReducer: mockMR,
			}

			env := &Env{
				Stderr:              &syncBuffer{},
				Getenv:              defaultTestEnv,
				RestructurerFactory: restructurerFactory,
			}

			_, err := RestructureContent(context.Background(), env, "content", RestructureOptions{
				Template: template.MustParseName("brainstorm"),
				Provider: tt.provider,
			})

			if err != nil {
				t.Fatalf("RestructureContent() unexpected error: %v", err)
			}

			calls := restructurerFactory.NewMapReducerCalls()
			if len(calls) != 1 {
				t.Fatalf("NewMapReducer() calls = %d, want 1", len(calls))
			}
			if calls[0].APIKey != tt.expectedKey {
				t.Errorf("NewMapReducer() apiKey = %q, want %q", calls[0].APIKey, tt.expectedKey)
			}
		})
	}
}
