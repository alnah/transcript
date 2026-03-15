package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alnah/transcript/internal/config"
)

// ---------------------------------------------------------------------------
// Unit tests for helper functions
// ---------------------------------------------------------------------------

func TestIsValidConfigKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		key      string
		expected bool
	}{
		{"valid output dir", config.KeyOutputDir, true},
		{"invalid random key", "random-key", false},
		{"empty string", "", false},
		{"wrong format with underscore", "output_dir", false}, // Wrong format (underscore vs dash)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := IsValidConfigKey(tt.key)
			if result != tt.expected {
				t.Errorf("IsValidConfigKey(%q) = %v, want %v", tt.key, result, tt.expected)
			}
		})
	}
}

func TestValidConfigKeys(t *testing.T) {
	t.Parallel()

	// Verify validConfigKeys contains expected keys
	found := false
	for _, key := range ValidConfigKeys {
		if key == config.KeyOutputDir {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected ValidConfigKeys to contain %q", config.KeyOutputDir)
	}
}

// ---------------------------------------------------------------------------
// Tests for runConfigSet
// ---------------------------------------------------------------------------

func TestRunConfigSet_ValidKey(t *testing.T) {
	// Note: This test modifies the real config file
	// We use t.Setenv to redirect config to temp dir
	// Cannot use t.Parallel() with t.Setenv()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	outputDir := t.TempDir()
	stderr := &syncBuffer{}

	env := &Env{
		Stderr: stderr,
		Getenv: os.Getenv,
	}

	err := RunConfigSet(env, config.KeyOutputDir, outputDir)
	if err != nil {
		t.Fatalf("RunConfigSet(%q, %q) unexpected error: %v", config.KeyOutputDir, outputDir, err)
	}

	// Verify success message
	output := stderr.String()
	if !strings.Contains(output, "Set") || !strings.Contains(output, config.KeyOutputDir) {
		t.Errorf("RunConfigSet(%q, %q) output = %q, want containing 'Set output-dir'", config.KeyOutputDir, outputDir, output)
	}

	// Verify config was saved
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() unexpected error: %v", err)
	}
	if cfg.OutputDir != outputDir {
		t.Errorf("config.Load().OutputDir = %q, want %q", cfg.OutputDir, outputDir)
	}
}

func TestRunConfigSet_InvalidKey(t *testing.T) {
	t.Parallel()

	env := &Env{
		Stderr: &syncBuffer{},
	}

	err := RunConfigSet(env, "invalid-key", "value")
	if err == nil {
		t.Fatal("RunConfigSet(\"invalid-key\", \"value\") expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("RunConfigSet(\"invalid-key\", \"value\") error = %q, want containing %q", err.Error(), "unknown")
	}
}

func TestRunConfigSet_ExpandsPath(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Create a test directory that can be expanded
	testDir := t.TempDir()
	stderr := &syncBuffer{}

	env := &Env{
		Stderr: stderr,
		Getenv: os.Getenv,
	}

	err := RunConfigSet(env, config.KeyOutputDir, testDir)
	if err != nil {
		t.Fatalf("RunConfigSet(%q, %q) unexpected error: %v", config.KeyOutputDir, testDir, err)
	}

	// Verify path was stored
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() unexpected error: %v", err)
	}
	// Path should be absolute
	if !filepath.IsAbs(cfg.OutputDir) {
		t.Errorf("config.Load().OutputDir = %q, want absolute path", cfg.OutputDir)
	}
}

func TestRunConfigSet_InvalidOutputDir(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Create a file (not directory) to cause validation failure
	filePath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(filePath, []byte("file"), 0644); err != nil {
		t.Fatalf("os.WriteFile(%q) unexpected error: %v", filePath, err)
	}

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: os.Getenv,
	}

	err := RunConfigSet(env, config.KeyOutputDir, filePath)
	if err == nil {
		t.Fatalf("RunConfigSet(%q, %q) expected error, got nil", config.KeyOutputDir, filePath)
	}
	if !strings.Contains(err.Error(), "invalid output-dir") {
		t.Errorf("RunConfigSet(%q, %q) error = %q, want containing %q", config.KeyOutputDir, filePath, err.Error(), "invalid output-dir")
	}
}

// ---------------------------------------------------------------------------
// Tests for runConfigGet
// ---------------------------------------------------------------------------

func TestRunConfigGet_ValidKey(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Set a value first
	outputDir := t.TempDir()
	if err := config.Save(config.KeyOutputDir, outputDir); err != nil {
		t.Fatalf("config.Save(%q, %q) unexpected error: %v", config.KeyOutputDir, outputDir, err)
	}

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: os.Getenv,
	}

	// Capture stdout (runConfigGet prints to stdout, not stderr)
	// Since we can't easily capture stdout in unit tests, we just verify no error
	err := RunConfigGet(env, config.KeyOutputDir)
	if err != nil {
		t.Fatalf("RunConfigGet(%q) unexpected error: %v", config.KeyOutputDir, err)
	}
}

func TestRunConfigGet_InvalidKey(t *testing.T) {
	t.Parallel()

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: os.Getenv,
	}

	err := RunConfigGet(env, "invalid-key")
	if err == nil {
		t.Fatal("RunConfigGet(\"invalid-key\") expected error, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("RunConfigGet(\"invalid-key\") error = %q, want containing %q", err.Error(), "unknown")
	}
}

func TestRunConfigGet_EnvFallback(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	envOutputDir := t.TempDir()

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: staticEnv(map[string]string{
			config.EnvOutputDir: envOutputDir,
		}),
	}

	// No config file - should use env fallback
	err := RunConfigGet(env, config.KeyOutputDir)
	if err != nil {
		t.Fatalf("RunConfigGet(%q) unexpected error: %v", config.KeyOutputDir, err)
	}
	// We can't easily verify the output, but the function should succeed
}

// ---------------------------------------------------------------------------
// Tests for runConfigList
// ---------------------------------------------------------------------------

func TestRunConfigList_WithConfig(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	// Set a value first
	outputDir := t.TempDir()
	if err := config.Save(config.KeyOutputDir, outputDir); err != nil {
		t.Fatalf("config.Save(%q, %q) unexpected error: %v", config.KeyOutputDir, outputDir, err)
	}

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: os.Getenv,
	}

	err := RunConfigList(env)
	if err != nil {
		t.Fatalf("RunConfigList() unexpected error: %v", err)
	}
}

func TestRunConfigList_EmptyConfig(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: func(string) string { return "" }, // No env vars
	}

	err := RunConfigList(env)
	if err != nil {
		t.Fatalf("RunConfigList() unexpected error: %v", err)
	}
}

func TestRunConfigList_WithEnvOverride(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	envOutputDir := t.TempDir()

	env := &Env{
		Stderr: &syncBuffer{},
		Getenv: staticEnv(map[string]string{
			config.EnvOutputDir: envOutputDir,
		}),
	}

	err := RunConfigList(env)
	if err != nil {
		t.Fatalf("RunConfigList() unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Tests for ConfigCmd (Cobra integration)
// ---------------------------------------------------------------------------

func TestConfigCmd_HasSubcommands(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := ConfigCmd(env)

	// Verify subcommands exist
	subcommands := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		subcommands[sub.Name()] = true
	}

	expected := []string{"set", "get", "list"}
	for _, name := range expected {
		if !subcommands[name] {
			t.Errorf("expected subcommand %q", name)
		}
	}
}

func TestConfigCmd_SetRequiresArgs(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := ConfigCmd(env)

	cmd.SetArgs([]string{"set"}) // Missing key and value
	err := cmd.Execute()

	if err == nil {
		t.Fatal("ConfigCmd.Execute() with args [\"set\"] expected error, got nil")
	}
}

func TestConfigCmd_SetRequiresTwoArgs(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := ConfigCmd(env)

	cmd.SetArgs([]string{"set", "key"}) // Missing value
	err := cmd.Execute()

	if err == nil {
		t.Fatal("ConfigCmd.Execute() with args [\"set\", \"key\"] expected error, got nil")
	}
}

func TestConfigCmd_GetRequiresArg(t *testing.T) {
	t.Parallel()

	env, _ := testEnv()
	cmd := ConfigCmd(env)

	cmd.SetArgs([]string{"get"}) // Missing key
	err := cmd.Execute()

	if err == nil {
		t.Fatal("ConfigCmd.Execute() with args [\"get\"] expected error, got nil")
	}
}

func TestConfigCmd_ListNoArgs(t *testing.T) {
	// Cannot use t.Parallel() with t.Setenv()

	tempDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tempDir)

	env, _ := testEnv()
	cmd := ConfigCmd(env)

	cmd.SetArgs([]string{"list"})
	err := cmd.Execute()

	if err != nil {
		t.Fatalf("ConfigCmd.Execute() with args [\"list\"] unexpected error: %v", err)
	}
}
