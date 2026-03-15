package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// Notes:
// - White-box testing (package config) to test internal parseFile function.
// - Uses t.TempDir() + t.Setenv("XDG_CONFIG_HOME") for I/O isolation.
// - Tests using t.Setenv are NOT parallel (incompatible with t.Parallel).
// - Pure functions (ResolveOutputPath, ExpandPath) use t.Parallel().
// - Permission tests (chmod) may behave differently on Windows.
//
// Coverage gaps (intentional - rare I/O errors not worth mocking):
// - os.UserHomeDir() failures in dir(), ExpandPath()
// - Non-NotExist errors in Load(), Get(), List()
// - Write errors in writeFile() (disk full, permission denied mid-write)
// These are system-level errors that would require extensive mocking for
// minimal benefit. The happy paths and common error cases are fully tested.

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// writeConfigFile creates a config file in the given directory.
func writeConfigFile(t *testing.T, dir, content string) {
	t.Helper()
	configDir := filepath.Join(dir, "transcript")
	if err := os.MkdirAll(configDir, 0750); err != nil {
		t.Fatalf("failed to create config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config")
	if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
}

// ---------------------------------------------------------------------------
// TestResolveOutputPath - Pure function for output path resolution
// ---------------------------------------------------------------------------

func TestResolveOutputPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		output      string
		outputDir   string
		defaultName string
		want        string
	}{
		// Case 1: Absolute path - used as-is
		{
			name:        "absolute path ignores outputDir",
			output:      "/absolute/path/file.txt",
			outputDir:   "/some/dir",
			defaultName: "default.txt",
			want:        "/absolute/path/file.txt",
		},
		{
			name:        "absolute path with empty outputDir",
			output:      "/absolute/path/file.txt",
			outputDir:   "",
			defaultName: "default.txt",
			want:        "/absolute/path/file.txt",
		},

		// Case 2: Relative path with outputDir
		{
			name:        "relative path joined with outputDir",
			output:      "subdir/file.txt",
			outputDir:   "/base/dir",
			defaultName: "default.txt",
			want:        "/base/dir/subdir/file.txt",
		},
		{
			name:        "relative path without outputDir",
			output:      "subdir/file.txt",
			outputDir:   "",
			defaultName: "default.txt",
			want:        "subdir/file.txt",
		},
		{
			name:        "filename only with outputDir",
			output:      "file.txt",
			outputDir:   "/base/dir",
			defaultName: "default.txt",
			want:        "/base/dir/file.txt",
		},

		// Case 3: Empty output - uses defaultName
		{
			name:        "empty output uses defaultName with outputDir",
			output:      "",
			outputDir:   "/base/dir",
			defaultName: "default.txt",
			want:        "/base/dir/default.txt",
		},
		{
			name:        "empty output uses defaultName without outputDir",
			output:      "",
			outputDir:   "",
			defaultName: "default.txt",
			want:        "default.txt",
		},

		// Edge cases: path cleaning
		{
			name:        "cleans redundant separators",
			output:      "subdir//file.txt",
			outputDir:   "/base//dir",
			defaultName: "default.txt",
			want:        "/base/dir/subdir/file.txt",
		},
		{
			name:        "cleans dot segments",
			output:      "./subdir/../file.txt",
			outputDir:   "/base/./dir",
			defaultName: "default.txt",
			want:        "/base/dir/file.txt",
		},
		{
			name:        "handles trailing slash in outputDir",
			output:      "file.txt",
			outputDir:   "/base/dir/",
			defaultName: "default.txt",
			want:        "/base/dir/file.txt",
		},

		// Edge cases: special values
		{
			name:        "dot as output",
			output:      ".",
			outputDir:   "/base/dir",
			defaultName: "default.txt",
			want:        "/base/dir",
		},
		{
			name:        "dot-dot as output",
			output:      "..",
			outputDir:   "/base/dir",
			defaultName: "default.txt",
			want:        "/base",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ResolveOutputPath(tt.output, tt.outputDir, tt.defaultName)
			if got != tt.want {
				t.Errorf("ResolveOutputPath(%q, %q, %q) = %q, want %q",
					tt.output, tt.outputDir, tt.defaultName, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestEnsureExtension - Pure function for extension enforcement
// ---------------------------------------------------------------------------

func TestEnsureExtension(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		ext  string
		want string
	}{
		// No extension - should add
		{"no extension simple", "notes", ".md", "notes.md"},
		{"no extension with path", "/path/to/notes", ".md", "/path/to/notes.md"},
		{"no extension relative", "dir/notes", ".md", "dir/notes.md"},

		// Already has .md - unchanged
		{"already has .md", "notes.md", ".md", "notes.md"},
		{"already has .md with path", "/path/notes.md", ".md", "/path/notes.md"},
		{"already has .MD uppercase", "notes.MD", ".md", "notes.MD"},

		// Has different extension - unchanged (not our job to modify)
		{"has .txt extension", "notes.txt", ".md", "notes.txt"},
		{"has .json extension", "data.json", ".md", "data.json"},
		{"has .ogg extension", "audio.ogg", ".md", "audio.ogg"},

		// Hidden files Unix - unchanged (filepath.Ext returns the "extension")
		{"hidden file", ".bashrc", ".md", ".bashrc"},
		{"hidden in path", "/home/.config", ".md", "/home/.config"},
		{"hidden with ext", ".notes.md", ".md", ".notes.md"},

		// Edge cases
		{"empty path", "", ".md", ".md"},
		{"dot in name", "notes.2024", ".md", "notes.2024"},
		{"multiple dots", "my.notes.backup", ".md", "my.notes.backup"},
		{"trailing dot", "notes.", ".md", "notes."},

		// Different extensions
		{"ogg extension", "audio", ".ogg", "audio.ogg"},
		{"txt extension", "file", ".txt", "file.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := EnsureExtension(tt.path, tt.ext)
			if got != tt.want {
				t.Errorf("EnsureExtension(%q, %q) = %q, want %q",
					tt.path, tt.ext, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestExpandPath - Pure function for ~ expansion
// ---------------------------------------------------------------------------

func TestExpandPath(t *testing.T) {
	t.Parallel()

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("cannot get home dir: %v", err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{
			name: "expands tilde prefix",
			path: "~/Documents/file.txt",
			want: filepath.Join(home, "Documents/file.txt"),
		},
		{
			name: "no expansion for absolute path",
			path: "/absolute/path",
			want: "/absolute/path",
		},
		{
			name: "no expansion for relative path",
			path: "relative/path",
			want: "relative/path",
		},
		{
			name: "no expansion for tilde in middle",
			path: "/path/~/file",
			want: "/path/~/file",
		},
		{
			name: "tilde alone expands to home",
			path: "~",
			want: home,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := ExpandPath(tt.path)
			if got != tt.want {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestLoad - Config loading with file and env precedence
// ---------------------------------------------------------------------------

func TestLoad(t *testing.T) {
	// NO t.Parallel() - uses t.Setenv

	t.Run("returns empty config when file missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		t.Setenv("TRANSCRIPT_OUTPUT_DIR", "")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if cfg.OutputDir != "" {
			t.Errorf("Load().OutputDir = %q, want empty", cfg.OutputDir)
		}
	})

	t.Run("reads output-dir from file", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		t.Setenv("TRANSCRIPT_OUTPUT_DIR", "")
		writeConfigFile(t, tmpDir, "output-dir=/from/file\n")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if cfg.OutputDir != "/from/file" {
			t.Errorf("OutputDir = %q, want %q", cfg.OutputDir, "/from/file")
		}
	})

	t.Run("falls back to env var when file empty", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		t.Setenv("TRANSCRIPT_OUTPUT_DIR", "/from/env")
		writeConfigFile(t, tmpDir, "# empty config\n")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if cfg.OutputDir != "/from/env" {
			t.Errorf("OutputDir = %q, want %q", cfg.OutputDir, "/from/env")
		}
	})

	t.Run("file takes precedence over env var", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		t.Setenv("TRANSCRIPT_OUTPUT_DIR", "/from/env")
		writeConfigFile(t, tmpDir, "output-dir=/from/file\n")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if cfg.OutputDir != "/from/file" {
			t.Errorf("OutputDir = %q, want %q (file should take precedence)", cfg.OutputDir, "/from/file")
		}
	})

	t.Run("env var used when key missing from file", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		t.Setenv("TRANSCRIPT_OUTPUT_DIR", "/from/env")
		writeConfigFile(t, tmpDir, "other-key=other-value\n")

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if cfg.OutputDir != "/from/env" {
			t.Errorf("OutputDir = %q, want %q", cfg.OutputDir, "/from/env")
		}
	})

	t.Run("returns error for invalid config syntax", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		t.Setenv("TRANSCRIPT_OUTPUT_DIR", "")
		writeConfigFile(t, tmpDir, "invalid-line-no-equals\n")

		_, err := Load()
		if err == nil {
			t.Error("Load() = nil, want error for invalid syntax")
		}
	})
}

// ---------------------------------------------------------------------------
// TestSave - Config persistence
// ---------------------------------------------------------------------------

func TestSave(t *testing.T) {
	// NO t.Parallel() - uses t.Setenv

	t.Run("creates config file when missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)

		err := Save("output-dir", "/new/path")
		if err != nil {
			t.Fatalf("Save(%q, %q) unexpected error: %v", "output-dir", "/new/path", err)
		}

		// Verify file was created
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if cfg.OutputDir != "/new/path" {
			t.Errorf("OutputDir = %q, want %q", cfg.OutputDir, "/new/path")
		}
	})

	t.Run("updates existing value", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		writeConfigFile(t, tmpDir, "output-dir=/old/path\n")

		err := Save("output-dir", "/new/path")
		if err != nil {
			t.Fatalf("Save(%q, %q) unexpected error: %v", "output-dir", "/new/path", err)
		}

		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load() unexpected error: %v", err)
		}
		if cfg.OutputDir != "/new/path" {
			t.Errorf("OutputDir = %q, want %q", cfg.OutputDir, "/new/path")
		}
	})

	t.Run("preserves other keys", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		writeConfigFile(t, tmpDir, "other-key=preserved\noutput-dir=/old\n")

		err := Save("output-dir", "/new")
		if err != nil {
			t.Fatalf("Save(%q, %q) unexpected error: %v", "output-dir", "/new", err)
		}

		data, err := List()
		if err != nil {
			t.Fatalf("List() unexpected error: %v", err)
		}
		if data["other-key"] != "preserved" {
			t.Errorf("other-key = %q, want %q", data["other-key"], "preserved")
		}
		if data["output-dir"] != "/new" {
			t.Errorf("output-dir = %q, want %q", data["output-dir"], "/new")
		}
	})

	t.Run("adds new key to existing file", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		writeConfigFile(t, tmpDir, "existing-key=value\n")

		err := Save("new-key", "new-value")
		if err != nil {
			t.Fatalf("Save(%q, %q) unexpected error: %v", "new-key", "new-value", err)
		}

		data, err := List()
		if err != nil {
			t.Fatalf("List() unexpected error: %v", err)
		}
		if data["existing-key"] != "value" {
			t.Errorf("existing-key = %q, want %q", data["existing-key"], "value")
		}
		if data["new-key"] != "new-value" {
			t.Errorf("new-key = %q, want %q", data["new-key"], "new-value")
		}
	})

	t.Run("rejects empty key", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)

		err := Save("", "value")
		if err == nil {
			t.Error("Save(\"\", \"value\") = nil, want error")
		}
		if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Save(\"\", \"value\") error = %v, want ErrInvalidKey", err)
		}
	})

	t.Run("rejects key with equals sign", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)

		err := Save("key=value", "value")
		if err == nil {
			t.Error("Save(\"key=value\", \"value\") = nil, want error")
		}
		if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Save(\"key=value\", \"value\") error = %v, want ErrInvalidKey", err)
		}
	})

	t.Run("rejects key with newline", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)

		err := Save("key\nvalue", "value")
		if err == nil {
			t.Error("Save(\"key\\nvalue\", \"value\") = nil, want error")
		}
		if !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Save(\"key\\nvalue\", \"value\") error = %v, want ErrInvalidKey", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestGet - Single value retrieval
// ---------------------------------------------------------------------------

func TestGet(t *testing.T) {
	// NO t.Parallel() - uses t.Setenv

	t.Run("returns value when key exists", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		writeConfigFile(t, tmpDir, "my-key=my-value\n")

		got, err := Get("my-key")
		if err != nil {
			t.Fatalf("Get(%q) unexpected error: %v", "my-key", err)
		}
		if got != "my-value" {
			t.Errorf("Get(%q) = %q, want %q", "my-key", got, "my-value")
		}
	})

	t.Run("returns empty when key missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		writeConfigFile(t, tmpDir, "other-key=value\n")

		got, err := Get("missing-key")
		if err != nil {
			t.Fatalf("Get(%q) unexpected error: %v", "missing-key", err)
		}
		if got != "" {
			t.Errorf("Get(%q) = %q, want empty", "missing-key", got)
		}
	})

	t.Run("returns empty when file missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		// No config file created

		got, err := Get("any-key")
		if err != nil {
			t.Fatalf("Get(%q) unexpected error: %v", "any-key", err)
		}
		if got != "" {
			t.Errorf("Get(%q) = %q, want empty", "any-key", got)
		}
	})

	t.Run("returns error for invalid config syntax", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		writeConfigFile(t, tmpDir, "invalid-no-equals\n")

		_, err := Get("any-key")
		if err == nil {
			t.Error("Get() = nil, want error for invalid syntax")
		}
	})
}

// ---------------------------------------------------------------------------
// TestList - All values retrieval
// ---------------------------------------------------------------------------

func TestList(t *testing.T) {
	// NO t.Parallel() - uses t.Setenv

	t.Run("returns all values", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		writeConfigFile(t, tmpDir, "key1=value1\nkey2=value2\n")

		got, err := List()
		if err != nil {
			t.Fatalf("List() unexpected error: %v", err)
		}
		if len(got) != 2 {
			t.Errorf("List() returned %d items, want 2", len(got))
		}
		if got["key1"] != "value1" {
			t.Errorf("key1 = %q, want %q", got["key1"], "value1")
		}
		if got["key2"] != "value2" {
			t.Errorf("key2 = %q, want %q", got["key2"], "value2")
		}
	})

	t.Run("returns empty map when file missing", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		// No config file created

		got, err := List()
		if err != nil {
			t.Fatalf("List() unexpected error: %v", err)
		}
		if got == nil {
			t.Error("List() returned nil, want empty map")
		}
		if len(got) != 0 {
			t.Errorf("List() returned %d items, want 0", len(got))
		}
	})

	t.Run("returns empty map for empty file", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		writeConfigFile(t, tmpDir, "")

		got, err := List()
		if err != nil {
			t.Fatalf("List() unexpected error: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("List() returned %d items, want 0", len(got))
		}
	})

	t.Run("returns error for invalid config syntax", func(t *testing.T) {
		tmpDir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", tmpDir)
		writeConfigFile(t, tmpDir, "invalid-no-equals\n")

		_, err := List()
		if err == nil {
			t.Error("List() = nil, want error for invalid syntax")
		}
	})
}

// ---------------------------------------------------------------------------
// TestEnsureOutputDir - Directory validation and creation
// ---------------------------------------------------------------------------

func TestEnsureOutputDir(t *testing.T) {
	// NO t.Parallel() - modifies filesystem

	t.Run("accepts existing writable directory", func(t *testing.T) {
		tmpDir := t.TempDir()

		err := EnsureOutputDir(tmpDir)
		if err != nil {
			t.Errorf("EnsureOutputDir(%q) = %v, want nil", tmpDir, err)
		}
	})

	t.Run("creates missing directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		newDir := filepath.Join(tmpDir, "new", "nested", "dir")

		err := EnsureOutputDir(newDir)
		if err != nil {
			t.Fatalf("EnsureOutputDir(%q) unexpected error: %v", newDir, err)
		}

		// Verify directory was created
		info, err := os.Stat(newDir)
		if err != nil {
			t.Fatalf("os.Stat(%q) unexpected error: %v", newDir, err)
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", newDir)
		}
	})

	t.Run("rejects empty path", func(t *testing.T) {
		err := EnsureOutputDir("")
		if err == nil {
			t.Error("EnsureOutputDir(\"\") = nil, want error")
		}
	})

	t.Run("rejects file path", func(t *testing.T) {
		tmpDir := t.TempDir()
		filePath := filepath.Join(tmpDir, "file.txt")
		if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
			t.Fatalf("failed to create test file: %v", err)
		}

		err := EnsureOutputDir(filePath)
		if err == nil {
			t.Errorf("EnsureOutputDir(%q) = nil, want error for file path", filePath)
		}
		if !errors.Is(err, ErrNotDirectory) {
			t.Errorf("EnsureOutputDir(%q) error = %v, want ErrNotDirectory", filePath, err)
		}
	})

	t.Run("expands tilde in path", func(t *testing.T) {
		// This test uses the real home directory
		// We create a temp subdir under home to test ~ expansion
		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot get home dir: %v", err)
		}

		// Create unique test dir name
		testDir := filepath.Join(home, ".transcript-test-ensure-output-dir")
		t.Cleanup(func() { os.RemoveAll(testDir) })

		err = EnsureOutputDir("~/.transcript-test-ensure-output-dir")
		if err != nil {
			t.Errorf("EnsureOutputDir with ~ = %v, want nil", err)
		}

		// Verify directory was created
		info, statErr := os.Stat(testDir)
		if statErr != nil {
			t.Fatalf("os.Stat(%q) unexpected error: %v", testDir, statErr)
		}
		if !info.IsDir() {
			t.Errorf("%q is not a directory", testDir)
		}
	})
}

// ---------------------------------------------------------------------------
// TestEnsureOutputDir_Permissions - Permission-related tests (Unix only)
// ---------------------------------------------------------------------------

func TestEnsureOutputDir_Permissions(t *testing.T) {
	// NO t.Parallel() - modifies filesystem permissions

	// Skip on Windows where chmod behaves differently
	if runtime.GOOS == "windows" {
		t.Skip("skipping permission tests on Windows")
	}

	t.Run("rejects non-writable directory", func(t *testing.T) {
		tmpDir := t.TempDir()
		readOnlyDir := filepath.Join(tmpDir, "readonly")
		if err := os.Mkdir(readOnlyDir, 0555); err != nil {
			t.Fatalf("failed to create readonly dir: %v", err)
		}
		t.Cleanup(func() {
			os.Chmod(readOnlyDir, 0755) // Restore for cleanup
		})

		err := EnsureOutputDir(readOnlyDir)
		if err == nil {
			t.Errorf("EnsureOutputDir(%q) = nil, want error for non-writable dir", readOnlyDir)
		}
		if !errors.Is(err, ErrNotWritable) {
			t.Errorf("EnsureOutputDir(%q) error = %v, want ErrNotWritable", readOnlyDir, err)
		}
	})

	t.Run("rejects when parent not writable", func(t *testing.T) {
		tmpDir := t.TempDir()
		readOnlyParent := filepath.Join(tmpDir, "readonly-parent")
		if err := os.Mkdir(readOnlyParent, 0555); err != nil {
			t.Fatalf("failed to create readonly parent: %v", err)
		}
		t.Cleanup(func() {
			os.Chmod(readOnlyParent, 0755) // Restore for cleanup
		})

		newDir := filepath.Join(readOnlyParent, "newdir")
		err := EnsureOutputDir(newDir)
		if err == nil {
			t.Errorf("EnsureOutputDir(%q) = nil, want error when parent not writable", newDir)
		}
	})
}

// ---------------------------------------------------------------------------
// TestParseFile - Internal parsing logic
// ---------------------------------------------------------------------------

func TestParseFile(t *testing.T) {
	// NO t.Parallel() - uses filesystem

	t.Run("parses key=value pairs", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config")
		content := "key1=value1\nkey2=value2\n"
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		got, err := parseFile(configPath)
		if err != nil {
			t.Fatalf("parseFile(%q) unexpected error: %v", configPath, err)
		}
		if got["key1"] != "value1" {
			t.Errorf("key1 = %q, want %q", got["key1"], "value1")
		}
		if got["key2"] != "value2" {
			t.Errorf("key2 = %q, want %q", got["key2"], "value2")
		}
	})

	t.Run("ignores comments", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config")
		content := "# This is a comment\nkey=value\n# Another comment\n"
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		got, err := parseFile(configPath)
		if err != nil {
			t.Fatalf("parseFile(%q) unexpected error: %v", configPath, err)
		}
		if len(got) != 1 {
			t.Errorf("parseFile() returned %d items, want 1", len(got))
		}
		if got["key"] != "value" {
			t.Errorf("key = %q, want %q", got["key"], "value")
		}
	})

	t.Run("ignores empty lines", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config")
		content := "\n\nkey=value\n\n"
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		got, err := parseFile(configPath)
		if err != nil {
			t.Fatalf("parseFile(%q) unexpected error: %v", configPath, err)
		}
		if len(got) != 1 {
			t.Errorf("parseFile() returned %d items, want 1", len(got))
		}
	})

	t.Run("trims whitespace around key and value", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config")
		content := "  key  =  value  \n"
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		got, err := parseFile(configPath)
		if err != nil {
			t.Fatalf("parseFile(%q) unexpected error: %v", configPath, err)
		}
		if got["key"] != "value" {
			t.Errorf("key = %q, want %q (should trim whitespace)", got["key"], "value")
		}
	})

	t.Run("handles value with equals sign", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config")
		content := "key=value=with=equals\n"
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		got, err := parseFile(configPath)
		if err != nil {
			t.Fatalf("parseFile(%q) unexpected error: %v", configPath, err)
		}
		if got["key"] != "value=with=equals" {
			t.Errorf("key = %q, want %q", got["key"], "value=with=equals")
		}
	})

	t.Run("returns error for invalid syntax", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "config")
		content := "invalid-line-without-equals\n"
		if err := os.WriteFile(configPath, []byte(content), 0644); err != nil {
			t.Fatalf("failed to write config: %v", err)
		}

		_, err := parseFile(configPath)
		if err == nil {
			t.Error("parseFile() = nil, want error for invalid syntax")
		}
		if !errors.Is(err, ErrInvalidSyntax) {
			t.Errorf("parseFile() error = %v, want ErrInvalidSyntax", err)
		}
	})

	t.Run("returns error for missing file", func(t *testing.T) {
		_, err := parseFile("/nonexistent/path/config")
		if err == nil {
			t.Error("parseFile() = nil, want error for missing file")
		}
	})
}

// ---------------------------------------------------------------------------
// TestDir - Internal directory resolution
// ---------------------------------------------------------------------------

func TestDir(t *testing.T) {
	// NO t.Parallel() - uses t.Setenv

	t.Run("uses XDG_CONFIG_HOME when set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/custom/config")

		got, err := dir()
		if err != nil {
			t.Fatalf("dir() unexpected error: %v", err)
		}
		want := "/custom/config/transcript"
		if got != want {
			t.Errorf("dir() = %q, want %q", got, want)
		}
	})

	t.Run("uses home/.config when XDG not set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")

		home, err := os.UserHomeDir()
		if err != nil {
			t.Skipf("cannot get home dir: %v", err)
		}

		got, err := dir()
		if err != nil {
			t.Fatalf("dir() unexpected error: %v", err)
		}
		want := filepath.Join(home, ".config", "transcript")
		if got != want {
			t.Errorf("dir() = %q, want %q", got, want)
		}
	})
}
