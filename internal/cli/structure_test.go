package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/alnah/transcript/internal/config"
	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/template"
)

// Notes:
// - Tests focus on observable behavior through public APIs (runStructure, StructureCmd)
// - File I/O is tested with real temp files; restructuring uses mocks
// - The mockRestructurerFactory from mocks_test.go is reused for consistency

// ---------------------------------------------------------------------------
// Tests for deriveStructuredOutputPath - Path transformation logic
// ---------------------------------------------------------------------------

func TestDeriveStructuredOutputPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"simple md file", "meeting.md", "meeting_structured.md"},
		{"removes raw suffix", "meeting_raw.md", "meeting_structured.md"},
		{"preserves extension", "notes.txt", "notes_structured.txt"},
		{"no extension", "transcript", "transcript_structured"},
		{"preserves path", "/path/to/meeting.md", "/path/to/meeting_structured.md"},
		{"path with raw suffix", "/path/to/notes_raw.md", "/path/to/notes_structured.md"},
		{"double extension", "file.backup.md", "file.backup_structured.md"},
		{"empty string", "", "_structured"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := DeriveStructuredOutputPath(tt.input)
			if result != tt.expected {
				t.Errorf("DeriveStructuredOutputPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests for ParseStructureOptions - CLI input parsing and validation
// ---------------------------------------------------------------------------

func TestParseStructureOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		inputPath  string
		output     string
		tmpl       string
		outputLang string
		provider   string
		wantErr    bool
		errContain string
	}{
		{
			name:      "valid minimal options",
			inputPath: "/path/to/file.md",
			tmpl:      "brainstorm",
			provider:  "deepseek",
			wantErr:   false,
		},
		{
			name:       "valid with all options",
			inputPath:  "/path/to/file.md",
			output:     "/output/file.md",
			tmpl:       "meeting",
			outputLang: "fr",
			provider:   "openai",
			wantErr:    false,
		},
		{
			name:       "invalid template",
			inputPath:  "/path/to/file.md",
			tmpl:       "nonexistent-template",
			provider:   "deepseek",
			wantErr:    true,
			errContain: "unknown",
		},
		{
			name:       "invalid language",
			inputPath:  "/path/to/file.md",
			tmpl:       "brainstorm",
			outputLang: "invalid-lang-code-too-long",
			provider:   "deepseek",
			wantErr:    true,
		},
		{
			name:       "invalid provider",
			inputPath:  "/path/to/file.md",
			tmpl:       "brainstorm",
			provider:   "invalid-provider",
			wantErr:    true,
			errContain: "invalid provider",
		},
		{
			name:      "empty provider uses default",
			inputPath: "/path/to/file.md",
			tmpl:      "brainstorm",
			provider:  "",
			wantErr:   false, // Empty provider is allowed - defaults to DeepSeek
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := ParseStructureOptions(tt.inputPath, tt.output, tt.tmpl, tt.outputLang, tt.provider)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ParseStructureOptions(%q, %q, %q, %q, %q) error = %v, wantErr %v", tt.inputPath, tt.output, tt.tmpl, tt.outputLang, tt.provider, err, tt.wantErr)
			}
			if tt.wantErr && tt.errContain != "" && !strings.Contains(err.Error(), tt.errContain) {
				t.Errorf("ParseStructureOptions(%q, %q, %q, %q, %q) error = %q, want containing %q", tt.inputPath, tt.output, tt.tmpl, tt.outputLang, tt.provider, err.Error(), tt.errContain)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests for StructureCmd - Cobra command creation and flag validation
// ---------------------------------------------------------------------------

func TestStructureCmd_RequiresFile(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := StructureCmd(env)

	cmd.SetArgs([]string{})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("StructureCmd.Execute() with no args: expected error, got nil")
	}
}

func TestStructureCmd_RequiresTemplate(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "test content")

	env, _ := testEnv()
	cmd := StructureCmd(env)

	cmd.SetArgs([]string{inputPath})
	err := cmd.Execute()

	if err == nil {
		t.Fatal("StructureCmd.Execute() without template flag: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "template") {
		t.Errorf("StructureCmd.Execute() error = %q, want containing %q", err.Error(), "template")
	}
}

func TestStructureCmd_DefaultProvider(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "test content")
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "output.md")

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
		FFmpegResolver:      &mockFFmpegResolver{},
		ConfigLoader:        &mockConfigLoader{},
		RestructurerFactory: restructurerFactory,
	}

	cmd := StructureCmd(env)
	cmd.SetArgs([]string{inputPath, "-t", "brainstorm", "-o", outputPath})
	err := cmd.Execute()

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify default provider was DeepSeek
	calls := restructurerFactory.NewMapReducerCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 NewMapReducer call, got %d", len(calls))
	}
	if calls[0].Provider != DeepSeekProvider {
		t.Errorf("expected default provider %q, got %q", DeepSeekProvider, calls[0].Provider)
	}
}

// ---------------------------------------------------------------------------
// Tests for runStructure - Core restructuring logic
// ---------------------------------------------------------------------------

// createStructureCmd creates a cobra.Command for testing runStructure.
func createStructureCmd(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	return cmd
}

// createTestTranscriptFile creates a temporary transcript file for testing.
func createTestTranscriptFile(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.md")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to create test transcript file: %v", err)
	}
	return path
}

// mustParseStructureOptions is a test helper that parses options or fails the test.
func mustParseStructureOptions(t *testing.T, inputPath, output, tmpl, outputLang, provider string) StructureOptions {
	t.Helper()
	opts, err := ParseStructureOptions(inputPath, output, tmpl, outputLang, provider)
	if err != nil {
		t.Fatalf("ParseStructureOptions failed: %v", err)
	}
	return opts
}

func TestRunStructure_FileNotFound(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, "/nonexistent/file.md", "", "brainstorm", "", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err == nil {
		t.Fatal("RunStructure() with nonexistent file: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("RunStructure() error = %q, want containing %q", err.Error(), "not found")
	}
}

func TestRunStructure_EmptyFile(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "")

	env, _ := testEnv()
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, "", "brainstorm", "", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err == nil {
		t.Fatal("RunStructure() with empty file: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("RunStructure() error = %q, want containing %q", err.Error(), "empty")
	}
}

func TestRunStructure_WhitespaceOnlyFile(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "   \n\t  \n  ")

	env, _ := testEnv()
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, "", "brainstorm", "", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err == nil {
		t.Fatal("RunStructure() with whitespace-only file: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("RunStructure() error = %q, want containing %q", err.Error(), "empty")
	}
}

func TestRunStructure_OutputExists(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "test content")
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "existing.md")

	// Create existing output file
	if err := os.WriteFile(outputPath, []byte("existing"), 0644); err != nil {
		t.Fatalf("failed to create existing file: %v", err)
	}

	env, _ := testEnv()
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, outputPath, "brainstorm", "", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err == nil {
		t.Fatal("RunStructure() with existing output file: expected error, got nil")
	}
	if !errors.Is(err, ErrOutputExists) {
		t.Errorf("RunStructure() error = %v, want ErrOutputExists", err)
	}
}

func TestRunStructure_MissingDeepSeekKey(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "test content")
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "output.md")

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: func(key string) string {
			if key == EnvOpenAIAPIKey {
				return "test-openai-key"
			}
			return "" // No DeepSeek key
		},
		ConfigLoader:        &mockConfigLoader{},
		RestructurerFactory: &mockRestructurerFactory{},
	}
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, outputPath, "brainstorm", "", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err == nil {
		t.Fatal("RunStructure() with missing DeepSeek API key: expected error, got nil")
	}
	if !errors.Is(err, ErrDeepSeekKeyMissing) {
		t.Errorf("RunStructure() error = %v, want ErrDeepSeekKeyMissing", err)
	}
}

func TestRunStructure_MissingOpenAIKey(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "test content")
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "output.md")

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: func(key string) string {
			if key == EnvDeepSeekAPIKey {
				return "test-deepseek-key"
			}
			return "" // No OpenAI key
		},
		ConfigLoader:        &mockConfigLoader{},
		RestructurerFactory: &mockRestructurerFactory{},
	}
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, outputPath, "brainstorm", "", "openai")
	err := RunStructure(cmd, env, opts)
	if err == nil {
		t.Fatal("RunStructure() with missing OpenAI API key: expected error, got nil")
	}
	if !errors.Is(err, ErrAPIKeyMissing) {
		t.Errorf("RunStructure() error = %v, want ErrAPIKeyMissing", err)
	}
}

func TestRunStructure_Success(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "This is the raw transcript content.")
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "output.md")
	stderr := &syncBuffer{}

	mockMR := &mockMapReduceRestructurer{
		RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
			return "# Restructured Output\n\nKey ideas here.", false, nil
		},
	}
	restructurerFactory := &mockRestructurerFactory{
		mockMapReducer: mockMR,
	}

	env := &Env{
		Stderr:              stderr,
		Getenv:              defaultTestEnv,
		ConfigLoader:        &mockConfigLoader{},
		RestructurerFactory: restructurerFactory,
	}
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, outputPath, "brainstorm", "", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err != nil {
		t.Fatalf("RunStructure() unexpected error: %v", err)
	}

	// Verify output file was created
	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("os.ReadFile(%q) unexpected error: %v", outputPath, err)
	}
	if !strings.Contains(string(content), "Restructured Output") {
		t.Errorf("output file content = %q, want containing %q", string(content), "Restructured Output")
	}

	// Verify stderr contains progress messages
	output := stderr.String()
	if !strings.Contains(output, "Reading") {
		t.Errorf("stderr output = %q, want containing %q", output, "Reading")
	}
	if !strings.Contains(output, "Restructuring") {
		t.Errorf("stderr output = %q, want containing %q", output, "Restructuring")
	}
	if !strings.Contains(output, "Done") {
		t.Errorf("stderr output = %q, want containing %q", output, "Done")
	}
}

func TestRunStructure_SuccessWithOpenAI(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "transcript content")
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "output.md")

	mockMR := &mockMapReduceRestructurer{
		RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
			return "restructured", false, nil
		},
	}
	restructurerFactory := &mockRestructurerFactory{
		mockMapReducer: mockMR,
	}

	// Only provide OpenAI key
	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: func(key string) string {
			if key == EnvOpenAIAPIKey {
				return "test-openai-key"
			}
			return ""
		},
		ConfigLoader:        &mockConfigLoader{},
		RestructurerFactory: restructurerFactory,
	}
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, outputPath, "meeting", "", "openai")
	err := RunStructure(cmd, env, opts)
	if err != nil {
		t.Fatalf("RunStructure() with OpenAI provider unexpected error: %v", err)
	}

	// Verify OpenAI provider was used
	calls := restructurerFactory.NewMapReducerCalls()
	if len(calls) != 1 {
		t.Fatalf("expected 1 NewMapReducer call, got %d", len(calls))
	}
	if calls[0].Provider != OpenAIProvider {
		t.Errorf("expected provider %q, got %q", OpenAIProvider, calls[0].Provider)
	}
}

func TestRunStructure_WithOutputLang(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "transcript content")
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "output.md")

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
		ConfigLoader:        &mockConfigLoader{},
		RestructurerFactory: restructurerFactory,
	}
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, outputPath, "meeting", "fr", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err != nil {
		t.Fatalf("RunStructure() unexpected error: %v", err)
	}

	if capturedLang.String() != "fr" {
		t.Errorf("captured language = %q, want %q", capturedLang.String(), "fr")
	}
}

func TestRunStructure_RestructureError(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "transcript content")
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "output.md")

	restructureErr := errors.New("API error during restructuring")
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
		ConfigLoader:        &mockConfigLoader{},
		RestructurerFactory: restructurerFactory,
	}
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, outputPath, "brainstorm", "", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err == nil {
		t.Fatal("RunStructure() with failing restructurer: expected error, got nil")
	}
	if !errors.Is(err, restructureErr) {
		t.Errorf("RunStructure() error = %v, want %v", err, restructureErr)
	}
}

func TestRunStructure_DefaultOutputPath(t *testing.T) {
	t.Parallel()

	inputDir := t.TempDir()
	inputPath := filepath.Join(inputDir, "meeting_raw.md")
	if err := os.WriteFile(inputPath, []byte("content"), 0644); err != nil {
		t.Fatalf("failed to create input file: %v", err)
	}

	outputDir := t.TempDir()

	mockMR := &mockMapReduceRestructurer{
		RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
			return "restructured", false, nil
		},
	}
	restructurerFactory := &mockRestructurerFactory{
		mockMapReducer: mockMR,
	}

	configLoader := &mockConfigLoader{
		LoadFunc: func() (config.Config, error) {
			return config.Config{OutputDir: outputDir}, nil
		},
	}

	env := &Env{
		Stderr:              &syncBuffer{},
		Getenv:              defaultTestEnv,
		ConfigLoader:        configLoader,
		RestructurerFactory: restructurerFactory,
	}
	cmd := createStructureCmd(context.Background())

	// Empty output path - should derive from input and use output-dir
	opts := mustParseStructureOptions(t, inputPath, "", "brainstorm", "", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err != nil {
		t.Fatalf("RunStructure() unexpected error: %v", err)
	}

	// Verify output was created with expected name (meeting_structured.md, not meeting_raw_structured.md)
	expectedPath := filepath.Join(outputDir, "meeting_structured.md")
	if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
		t.Errorf("output file not found at %s", expectedPath)
	}
}

func TestRunStructure_ProgressCallback(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "transcript content")
	outputDir := t.TempDir()
	outputPath := filepath.Join(outputDir, "output.md")
	stderr := &syncBuffer{}

	mockMR := &mockMapReduceRestructurer{
		RestructureFunc: func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
			return "restructured", false, nil
		},
	}
	restructurerFactory := &mockRestructurerFactory{
		mockMapReducer: mockMR,
	}

	env := &Env{
		Stderr:              stderr,
		Getenv:              defaultTestEnv,
		ConfigLoader:        &mockConfigLoader{},
		RestructurerFactory: restructurerFactory,
	}
	cmd := createStructureCmd(context.Background())

	opts := mustParseStructureOptions(t, inputPath, outputPath, "brainstorm", "", "deepseek")
	err := RunStructure(cmd, env, opts)
	if err != nil {
		t.Fatalf("RunStructure() unexpected error: %v", err)
	}

	// Verify restructuring message includes provider
	output := stderr.String()
	if !strings.Contains(output, "deepseek") {
		t.Errorf("stderr output = %q, want containing %q", output, "deepseek")
	}
}

// ---------------------------------------------------------------------------
// Tests for extension warning
// ---------------------------------------------------------------------------

func TestRunStructure_NonMdExtensionWarning(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "transcript content")
	outputDir := t.TempDir()
	env, getStderr := structureEnvForExtensionTest(t)
	cmd := createStructureCmd(context.Background())

	// Use .txt extension - should trigger warning
	outputPath := filepath.Join(outputDir, "output.txt")
	opts := mustParseStructureOptions(t, inputPath, outputPath, "brainstorm", "", "deepseek")
	if err := RunStructure(cmd, env, opts); err != nil {
		t.Fatalf("RunStructure() unexpected error: %v", err)
	}

	// Verify warning was emitted
	output := getStderr()
	if !strings.Contains(output, "Warning") || !strings.Contains(output, ".txt") {
		t.Errorf("stderr output = %q, want containing both %q and %q", output, "Warning", ".txt")
	}
}

func TestRunStructure_MdExtensionNoWarning(t *testing.T) {
	t.Parallel()

	inputPath := createTestTranscriptFile(t, "transcript content")
	outputDir := t.TempDir()
	env, getStderr := structureEnvForExtensionTest(t)
	cmd := createStructureCmd(context.Background())

	// Use .md extension - should NOT trigger warning
	outputPath := filepath.Join(outputDir, "output.md")
	opts := mustParseStructureOptions(t, inputPath, outputPath, "brainstorm", "", "deepseek")
	if err := RunStructure(cmd, env, opts); err != nil {
		t.Fatalf("RunStructure() unexpected error: %v", err)
	}

	// Verify NO warning about extension
	output := getStderr()
	if strings.Contains(output, "regardless") {
		t.Errorf("stderr output = %q, should not contain extension warning for .md file", output)
	}
}

// ---------------------------------------------------------------------------
// Tests for validation order in runStructure
// ---------------------------------------------------------------------------

func TestRunStructure_ValidationOrder(t *testing.T) {
	t.Parallel()

	t.Run("file not found first", func(t *testing.T) {
		t.Parallel()

		env, _ := testEnv()
		cmd := createStructureCmd(context.Background())

		opts := mustParseStructureOptions(t, "/nonexistent/path.md", "", "brainstorm", "", "deepseek")
		err := RunStructure(cmd, env, opts)
		if err == nil {
			t.Fatal("RunStructure() with nonexistent file: expected error, got nil")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("RunStructure() error = %q, want containing %q", err.Error(), "not found")
		}
	})
}
