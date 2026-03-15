package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Config keys.
const (
	KeyOutputDir = "output-dir"
)

// Config directory names.
const (
	configDirName       = "transcript"
	legacyConfigDirName = "go-transcript"
	configFileName      = "config"
)

// Environment variable fallbacks.
const (
	EnvOutputDir = "TRANSCRIPT_OUTPUT_DIR"
)

// File system permissions.
const (
	dirPerm  os.FileMode = 0750
	filePerm os.FileMode = 0644
)

// Sentinel errors for error handling with errors.Is().
var (
	// ErrInvalidSyntax is returned when the config file has invalid syntax.
	ErrInvalidSyntax = errors.New("invalid config syntax")
	// ErrInvalidKey is returned when a config key contains invalid characters.
	ErrInvalidKey = errors.New("invalid config key")
	// ErrNotWritable is returned when a directory is not writable.
	ErrNotWritable = errors.New("directory not writable")
	// ErrNotDirectory is returned when a path is not a directory.
	ErrNotDirectory = errors.New("path is not a directory")
)

// Config holds user configuration loaded from ~/.config/transcript/config.
type Config struct {
	OutputDir string
}

// dir returns the configuration directory path.
// Uses XDG_CONFIG_HOME if set, otherwise ~/.config/transcript.
func dir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, configDirName), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", configDirName), nil
}

// legacyDir returns the previous configuration directory path used before rename.
func legacyDir() (string, error) {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, legacyConfigDirName), nil
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", legacyConfigDirName), nil
}

// path returns the full path to the config file.
func path() (string, error) {
	d, err := dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, configFileName), nil
}

// legacyPath returns the previous config file path used before rename.
func legacyPath() (string, error) {
	d, err := legacyDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, configFileName), nil
}

// readPath returns the config file to read.
// Preference order:
//  1. New path (~/.config/transcript/config)
//  2. Legacy path (~/.config/go-transcript/config) for backward compatibility
//  3. New path (when neither exists, callers handle os.IsNotExist)
func readPath() (string, error) {
	p, err := path()
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(p); err == nil {
		return p, nil
	} else if !os.IsNotExist(err) {
		return p, nil
	}

	legacy, err := legacyPath()
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(legacy); err == nil {
		return legacy, nil
	} else if !os.IsNotExist(err) {
		return legacy, nil
	}

	return p, nil
}

// Load reads the configuration file and environment variables.
// Precedence: config file values, then environment variable fallbacks.
// Returns an empty Config if the file doesn't exist (not an error).
func Load() (Config, error) {
	var cfg Config

	p, err := readPath()
	if err != nil {
		return cfg, err
	}

	// Read config file if it exists.
	if data, err := parseFile(p); err == nil {
		cfg.OutputDir = data[KeyOutputDir]
	} else if !os.IsNotExist(err) {
		return cfg, fmt.Errorf("failed to read config: %w", err)
	}

	// Environment variable fallback (only if not set in config).
	if cfg.OutputDir == "" {
		cfg.OutputDir = os.Getenv(EnvOutputDir)
	}

	return cfg, nil
}

// parseFile reads a key=value config file.
// Format: one key=value per line, # comments, empty lines ignored.
func parseFile(path string) (map[string]string, error) {
	f, err := os.Open(path) // #nosec G304 -- config path is constructed from home dir
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	data := make(map[string]string)
	scanner := bufio.NewScanner(f)
	lineNum := 0

	for scanner.Scan() {
		lineNum++
		line := strings.TrimSpace(scanner.Text())

		// Skip empty lines and comments.
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse key=value.
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("%w: %s:%d: %q", ErrInvalidSyntax, path, lineNum, line)
		}

		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])
		data[key] = value
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", path, err)
	}

	return data, nil
}

// Save writes a single key=value to the config file.
// Creates the config directory and file if they don't exist.
// Preserves existing key=value pairs but discards comments.
// Returns ErrInvalidKey if the key contains = or newline characters.
//
// WARNING: This function rewrites the entire config file. Any comments
// (lines starting with #) in the original file will be lost. This is a
// known limitation of the current implementation.
func Save(key, value string) error {
	// Validate key to prevent config file corruption.
	if strings.ContainsAny(key, "=\n\r") || key == "" {
		return fmt.Errorf("%w: %q", ErrInvalidKey, key)
	}

	configPath, err := path()
	if err != nil {
		return err
	}

	readConfigPath, err := readPath()
	if err != nil {
		return err
	}

	// Ensure config directory exists.
	configDir := filepath.Dir(configPath)
	if err := os.MkdirAll(configDir, dirPerm); err != nil { // #nosec G301 -- user config dir
		return fmt.Errorf("cannot create config directory: %w", err)
	}

	// Read existing config (if any), with legacy fallback.
	existing, _ := parseFile(readConfigPath)
	if existing == nil {
		existing = make(map[string]string)
	}

	// Update value.
	existing[key] = value

	// Write back (WARNING: comments are not preserved).
	return writeFile(configPath, existing)
}

// writeFile writes the config map to a file.
// Keys are sorted alphabetically for deterministic output.
func writeFile(path string, data map[string]string) error {
	// #nosec G302 G304 -- config file with standard permissions, path from home dir
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, filePerm)
	if err != nil {
		return fmt.Errorf("cannot write config file: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Sort keys for deterministic output.
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if _, err := fmt.Fprintf(f, "%s=%s\n", key, data[key]); err != nil {
			return fmt.Errorf("failed to write config: %w", err)
		}
	}

	return nil
}

// Get reads a single value from the config file.
// Returns empty string if the key doesn't exist.
func Get(key string) (string, error) {
	p, err := readPath()
	if err != nil {
		return "", err
	}

	data, err := parseFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}

	return data[key], nil
}

// List returns all config values as a map.
func List() (map[string]string, error) {
	p, err := readPath()
	if err != nil {
		return nil, err
	}

	data, err := parseFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, err
	}

	return data, nil
}

// ResolveOutputPath resolves the final output path using the following precedence:
//  1. If output is absolute, use it as-is
//  2. If output is relative and outputDir is set, join them
//  3. If output is empty, use defaultName in outputDir (or cwd if no outputDir)
//
// outputDir can come from config or flag.
// All paths are cleaned using filepath.Clean to normalize separators and remove redundant elements.
func ResolveOutputPath(output, outputDir, defaultName string) string {
	// Case 1: Explicit absolute path - use as-is.
	if output != "" && filepath.IsAbs(output) {
		return filepath.Clean(output)
	}

	// Case 2: Explicit relative path - combine with outputDir if set.
	if output != "" {
		if outputDir != "" {
			return filepath.Clean(filepath.Join(outputDir, output))
		}
		return filepath.Clean(output)
	}

	// Case 3: No output specified - use default name.
	if outputDir != "" {
		return filepath.Clean(filepath.Join(outputDir, defaultName))
	}
	return filepath.Clean(defaultName)
}

// ExpandPath expands ~ or ~/path to the user's home directory.
// Returns the path unchanged if expansion fails or if it doesn't start with ~.
func ExpandPath(path string) string {
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

// EnsureExtension adds ext to path if path has no extension.
// If path already has an extension (including hidden files like ".bashrc"),
// it is returned unchanged.
//
// Examples:
//
//	EnsureExtension("notes", ".md")           → "notes.md"
//	EnsureExtension("notes.md", ".md")        → "notes.md"
//	EnsureExtension("notes.txt", ".md")       → "notes.txt"
//	EnsureExtension(".bashrc", ".md")         → ".bashrc"
//	EnsureExtension("/path/to/notes", ".md")  → "/path/to/notes.md"
func EnsureExtension(path, ext string) string {
	if filepath.Ext(path) == "" {
		return path + ext
	}
	return path
}

// EnsureOutputDir validates a directory path and creates it if it doesn't exist.
// Returns nil if the directory exists and is writable, or was successfully created.
// Returns an error describing the problem otherwise.
//
// Note: Tilde expansion is performed via ExpandPath. If expansion fails (e.g.,
// home directory cannot be determined), the path is used as-is, which will
// likely result in an error from os.Stat or os.MkdirAll.
func EnsureOutputDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("output-dir cannot be empty")
	}

	// Expand ~ to home directory (uses ExpandPath to avoid duplication).
	dir = ExpandPath(dir)

	// Check if path exists.
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// Directory doesn't exist - try to create it.
			if err := os.MkdirAll(dir, dirPerm); err != nil { // #nosec G301 -- user output dir
				return fmt.Errorf("cannot create directory: %w", err)
			}
			return nil
		}
		return fmt.Errorf("cannot access directory: %w", err)
	}

	// Check if it's a directory.
	if !info.IsDir() {
		return fmt.Errorf("%w: %s", ErrNotDirectory, dir)
	}

	// Check if writable by attempting to create a temp file.
	testFile := filepath.Join(dir, ".transcript-write-test")
	f, err := os.Create(testFile) // #nosec G304 -- path is constructed from validated dir
	if err != nil {
		return fmt.Errorf("%w: %s", ErrNotWritable, dir)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(testFile)
		return fmt.Errorf("%w: %s", ErrNotWritable, dir)
	}
	_ = os.Remove(testFile) // Best effort cleanup, ignore error

	return nil
}
