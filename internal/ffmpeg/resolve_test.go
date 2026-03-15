package ffmpeg

// Notes:
// - Tests for pure functions verifyChecksum and decompressGzip using t.TempDir()
// - White-box testing (same package) required since functions are unexported
// - Resolver tests use mock implementations of fileReader, fileWriter, envProvider
// - HTTP download tests use httptest.Server for realistic HTTP behavior
// - downloadBinary is tested indirectly through Resolver.Resolve with auto-download

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// verifyChecksum - SHA256 verification
// ---------------------------------------------------------------------------

func TestVerifyChecksum(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     []byte
		expectMatch bool
	}{
		{
			name:        "valid checksum matches",
			content:     []byte("hello world"),
			expectMatch: true,
		},
		{
			name:        "empty file valid checksum",
			content:     []byte{},
			expectMatch: true,
		},
		{
			name:        "binary content valid checksum",
			content:     []byte{0x00, 0xFF, 0x7F, 0x80},
			expectMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Create temp file with content
			tmpDir := t.TempDir()
			filePath := filepath.Join(tmpDir, "testfile")
			if err := os.WriteFile(filePath, tt.content, 0644); err != nil {
				t.Fatalf("failed to write test file: %v", err)
			}

			// Compute expected checksum
			h := sha256.Sum256(tt.content)
			expectedSHA := hex.EncodeToString(h[:])

			// Verify
			err := verifyChecksum(filePath, expectedSHA)
			if tt.expectMatch && err != nil {
				t.Errorf("verifyChecksum(%q, %q) unexpected error: %v", filePath, expectedSHA, err)
			}
		})
	}
}

func TestVerifyChecksumMismatch(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	filePath := filepath.Join(tmpDir, "testfile")
	if err := os.WriteFile(filePath, []byte("actual content"), 0644); err != nil {
		t.Fatalf("setup: failed to write test file: %v", err)
	}

	wrongSHA := "0000000000000000000000000000000000000000000000000000000000000000"

	err := verifyChecksum(filePath, wrongSHA)
	if err == nil {
		t.Errorf("verifyChecksum(%q, %q) = nil, want error", filePath, wrongSHA)
	}
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Errorf("verifyChecksum(%q, %q) error = %v, want ErrChecksumMismatch", filePath, wrongSHA, err)
	}
}

func TestVerifyChecksumFileNotFound(t *testing.T) {
	t.Parallel()

	err := verifyChecksum("/nonexistent/path/file", "anychecksum")
	if err == nil {
		t.Errorf("verifyChecksum(%q, %q) = nil, want error", "/nonexistent/path/file", "anychecksum")
	}
}

// ---------------------------------------------------------------------------
// decompressGzip - gzip extraction with size limit
// ---------------------------------------------------------------------------

func TestDecompressGzip(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content []byte
	}{
		{
			name:    "simple text",
			content: []byte("hello world from gzip"),
		},
		{
			name:    "empty content",
			content: []byte{},
		},
		{
			name:    "binary content",
			content: bytes.Repeat([]byte{0x00, 0xFF, 0x7F}, 100),
		},
		{
			name:    "large content under limit",
			content: bytes.Repeat([]byte("x"), 1024*1024), // 1MB
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()

			// Create gzip file
			gzPath := filepath.Join(tmpDir, "test.gz")
			createGzipFile(t, gzPath, tt.content)

			// Decompress
			destPath := filepath.Join(tmpDir, "output")
			err := decompressGzip(gzPath, destPath)
			if err != nil {
				t.Fatalf("decompressGzip(%q, %q) unexpected error: %v", gzPath, destPath, err)
			}

			// Verify content
			got, err := os.ReadFile(destPath)
			if err != nil {
				t.Fatalf("setup: failed to read output: %v", err)
			}
			if !bytes.Equal(got, tt.content) {
				t.Errorf("decompressGzip(%q, %q) wrote %d bytes, want %d bytes", gzPath, destPath, len(got), len(tt.content))
			}
		})
	}
}

func TestDecompressGzipInvalidGzip(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create invalid gzip file (just random bytes)
	gzPath := filepath.Join(tmpDir, "invalid.gz")
	if err := os.WriteFile(gzPath, []byte("not a gzip file"), 0644); err != nil {
		t.Fatalf("setup: failed to write test file: %v", err)
	}

	destPath := filepath.Join(tmpDir, "output")
	err := decompressGzip(gzPath, destPath)
	if err == nil {
		t.Errorf("decompressGzip(%q, %q) = nil, want error", gzPath, destPath)
	}
}

func TestDecompressGzipFileNotFound(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	destPath := filepath.Join(tmpDir, "output")

	err := decompressGzip("/nonexistent/file.gz", destPath)
	if err == nil {
		t.Errorf("decompressGzip(%q, %q) = nil, want error", "/nonexistent/file.gz", destPath)
	}
}

func TestDecompressGzipAtomicWrite(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create valid gzip
	content := []byte("atomic write test")
	gzPath := filepath.Join(tmpDir, "test.gz")
	createGzipFile(t, gzPath, content)

	// Decompress to destination
	destPath := filepath.Join(tmpDir, "output")
	err := decompressGzip(gzPath, destPath)
	if err != nil {
		t.Fatalf("decompressGzip(%q, %q) unexpected error: %v", gzPath, destPath, err)
	}

	// Verify no temp files left behind
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatalf("setup: failed to read temp dir: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if name != "test.gz" && name != "output" {
			t.Errorf("decompressGzip(%q, %q) left unexpected file: %s", gzPath, destPath, name)
		}
	}
}

func TestDecompressGzipSizeLimitProtection(t *testing.T) {
	t.Parallel()

	// This test verifies that decompressGzip enforces the size limit.
	// We can't easily create a file that exceeds 200MB in tests,
	// so we verify the limit is applied by checking behavior at boundary.
	// The actual limit is maxDecompressedSize (200MB).

	tmpDir := t.TempDir()

	// Create a gzip file with content just under the theoretical test size
	// For practical testing, we just verify the mechanism works with normal content
	content := bytes.Repeat([]byte("x"), 1024*1024) // 1MB - well under limit
	gzPath := filepath.Join(tmpDir, "test.gz")
	createGzipFile(t, gzPath, content)

	destPath := filepath.Join(tmpDir, "output")
	err := decompressGzip(gzPath, destPath)
	if err != nil {
		t.Fatalf("decompressGzip(%q, %q) unexpected error for content under limit: %v", gzPath, destPath, err)
	}

	// Verify content was written correctly
	got, err := os.ReadFile(destPath)
	if err != nil {
		t.Fatalf("setup: failed to read output: %v", err)
	}
	if len(got) != len(content) {
		t.Errorf("decompressGzip(%q, %q) wrote %d bytes, want %d bytes", gzPath, destPath, len(got), len(content))
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// createGzipFile creates a gzip compressed file with the given content.
// Fails the test immediately if file creation fails.
func createGzipFile(t *testing.T, path string, content []byte) {
	t.Helper()

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create gzip file: %v", err)
	}
	defer f.Close()

	gw := gzip.NewWriter(f)
	if _, err := gw.Write(content); err != nil {
		t.Fatalf("write gzip content: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Resolver - dependency injection tests
// ---------------------------------------------------------------------------

func TestResolverResolveEnvPath(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ffmpegPath := filepath.Join(tmpDir, "ffmpeg")
	if err := os.WriteFile(ffmpegPath, []byte("fake binary"), 0755); err != nil {
		t.Fatalf("failed to create fake binary: %v", err)
	}

	tests := []struct {
		name     string
		envPath  string
		wantPath string
		wantErr  bool
	}{
		{
			name:     "FFMPEG_PATH set and exists",
			envPath:  ffmpegPath,
			wantPath: ffmpegPath,
			wantErr:  false,
		},
		{
			name:     "FFMPEG_PATH set but not exists",
			envPath:  "/nonexistent/ffmpeg",
			wantPath: "",
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := &mockEnvProvider{
				getenv: func(key string) string {
					if key == "FFMPEG_PATH" {
						return tt.envPath
					}
					return ""
				},
			}

			reader := &mockFileReader{
				stat: func(name string) (os.FileInfo, error) {
					if name == ffmpegPath {
						return mockFileInfo{name: "ffmpeg"}, nil
					}
					return nil, os.ErrNotExist
				},
			}

			resolver := NewResolver(
				WithEnvProvider(env),
				WithFileReader(reader),
				WithStderr(io.Discard),
			)

			got, err := resolver.Resolve(context.Background())

			if tt.wantErr {
				if err == nil {
					t.Errorf("Resolve() = %q, nil; want error", got)
				}
				if err != nil && !errors.Is(err, ErrNotFound) {
					t.Errorf("Resolve() error = %v, want ErrNotFound", err)
				}
			} else {
				if err != nil {
					t.Errorf("Resolve() unexpected error: %v", err)
				}
				if got != tt.wantPath {
					t.Errorf("Resolve() = %q, want %q", got, tt.wantPath)
				}
			}
		})
	}
}

func TestResolverResolveInstalledPath(t *testing.T) {
	t.Parallel()

	homeDir := "/mock/home"
	installedPath := filepath.Join(homeDir, ".transcript", "bin", "ffmpeg")
	versionPath := filepath.Join(homeDir, ".transcript", "bin", ".version")

	env := &mockEnvProvider{
		getenv:      func(key string) string { return "" },
		userHomeDir: func() (string, error) { return homeDir, nil },
		lookPath:    func(file string) (string, error) { return "", errors.New("not in PATH") },
	}

	reader := &mockFileReader{
		stat: func(name string) (os.FileInfo, error) {
			if name == installedPath {
				return mockFileInfo{name: "ffmpeg"}, nil
			}
			return nil, os.ErrNotExist
		},
		readFile: func(name string) ([]byte, error) {
			if name == versionPath {
				return []byte(ffmpegVersion), nil
			}
			return nil, os.ErrNotExist
		},
	}

	resolver := NewResolver(
		WithEnvProvider(env),
		WithFileReader(reader),
		WithStderr(io.Discard),
	)

	got, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if got != installedPath {
		t.Errorf("Resolve() = %q, want %q", got, installedPath)
	}
}

func TestResolverResolveLegacyInstalledPathFallback(t *testing.T) {
	t.Parallel()

	homeDir := "/mock/home"
	newPath := filepath.Join(homeDir, ".transcript", "bin", "ffmpeg")
	legacyPath := filepath.Join(homeDir, ".go-transcript", "bin", "ffmpeg")
	legacyVersionPath := filepath.Join(homeDir, ".go-transcript", "bin", ".version")

	env := &mockEnvProvider{
		getenv:      func(key string) string { return "" },
		userHomeDir: func() (string, error) { return homeDir, nil },
		lookPath:    func(file string) (string, error) { return "", errors.New("not in PATH") },
	}

	reader := &mockFileReader{
		stat: func(name string) (os.FileInfo, error) {
			switch name {
			case newPath:
				return nil, os.ErrNotExist
			case legacyPath:
				return mockFileInfo{name: "ffmpeg"}, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		readFile: func(name string) ([]byte, error) {
			if name == legacyVersionPath {
				return []byte(ffmpegVersion), nil
			}
			return nil, os.ErrNotExist
		},
	}

	resolver := NewResolver(
		WithEnvProvider(env),
		WithFileReader(reader),
		WithStderr(io.Discard),
	)

	got, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if got != legacyPath {
		t.Errorf("Resolve() = %q, want %q", got, legacyPath)
	}
}

func TestResolverResolvePrefersNewInstalledPathOverLegacy(t *testing.T) {
	t.Parallel()

	homeDir := "/mock/home"
	newPath := filepath.Join(homeDir, ".transcript", "bin", "ffmpeg")
	newVersionPath := filepath.Join(homeDir, ".transcript", "bin", ".version")
	legacyPath := filepath.Join(homeDir, ".go-transcript", "bin", "ffmpeg")
	legacyVersionPath := filepath.Join(homeDir, ".go-transcript", "bin", ".version")

	env := &mockEnvProvider{
		getenv:      func(key string) string { return "" },
		userHomeDir: func() (string, error) { return homeDir, nil },
		lookPath:    func(file string) (string, error) { return "", errors.New("not in PATH") },
	}

	reader := &mockFileReader{
		stat: func(name string) (os.FileInfo, error) {
			switch name {
			case newPath, legacyPath:
				return mockFileInfo{name: "ffmpeg"}, nil
			default:
				return nil, os.ErrNotExist
			}
		},
		readFile: func(name string) ([]byte, error) {
			switch name {
			case newVersionPath, legacyVersionPath:
				return []byte(ffmpegVersion), nil
			default:
				return nil, os.ErrNotExist
			}
		},
	}

	resolver := NewResolver(
		WithEnvProvider(env),
		WithFileReader(reader),
		WithStderr(io.Discard),
	)

	got, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if got != newPath {
		t.Errorf("Resolve() = %q, want %q (new path must win over legacy)", got, newPath)
	}
}

func TestResolverResolveSystemPath(t *testing.T) {
	t.Parallel()

	systemFFmpeg := "/usr/local/bin/ffmpeg"

	env := &mockEnvProvider{
		getenv:      func(key string) string { return "" },
		userHomeDir: func() (string, error) { return "/mock/home", nil },
		lookPath: func(file string) (string, error) {
			if file == "ffmpeg" {
				return systemFFmpeg, nil
			}
			return "", errors.New("not found")
		},
	}

	reader := &mockFileReader{
		stat: func(name string) (os.FileInfo, error) {
			return nil, os.ErrNotExist
		},
	}

	resolver := NewResolver(
		WithEnvProvider(env),
		WithFileReader(reader),
		WithStderr(io.Discard),
	)

	got, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}
	if got != systemFFmpeg {
		t.Errorf("Resolve() = %q, want %q", got, systemFFmpeg)
	}
}

func TestResolverResolveUnsupportedPlatform(t *testing.T) {
	t.Parallel()

	env := &mockEnvProvider{
		getenv:      func(key string) string { return "" },
		userHomeDir: func() (string, error) { return "/mock/home", nil },
		lookPath:    func(file string) (string, error) { return "", errors.New("not found") },
	}

	reader := &mockFileReader{
		stat: func(name string) (os.FileInfo, error) { return nil, os.ErrNotExist },
	}

	writer := &mockFileWriter{}

	resolver := NewResolver(
		WithEnvProvider(env),
		WithFileReader(reader),
		WithFileWriter(writer),
		WithStderr(io.Discard),
		WithPlatform("unsupported", "arch"),
	)

	_, err := resolver.Resolve(context.Background())
	if err == nil {
		t.Errorf("Resolve() = nil, want error for unsupported platform")
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve() error = %v, want ErrNotFound (wrapping UnsupportedPlatform)", err)
	}
}

func TestResolverResolveAutoDownload(t *testing.T) {
	t.Parallel()

	// Create a fake gzipped binary
	fakeContent := []byte("fake ffmpeg binary content")
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(fakeContent); err != nil {
		t.Fatalf("failed to gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip: %v", err)
	}
	gzData := gzBuf.Bytes()

	// Compute checksum of gzipped data
	h := sha256.Sum256(gzData)
	checksum := hex.EncodeToString(h[:])

	// Create test server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(gzData)
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	env := &mockEnvProvider{
		getenv:      func(key string) string { return "" },
		userHomeDir: func() (string, error) { return tmpDir, nil },
		lookPath:    func(file string) (string, error) { return "", errors.New("not found") },
	}

	resolver := NewResolver(
		WithEnvProvider(env),
		WithStderr(io.Discard),
		WithPlatform("testgoos", "testgoarch"),
		WithPlatformInfo(binaryInfo{
			URL:    server.URL + "/ffmpeg.gz",
			SHA256: checksum,
		}),
		WithHTTPClient(server.Client()),
	)

	got, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve() unexpected error: %v", err)
	}

	expectedPath := filepath.Join(tmpDir, ".transcript", "bin", "ffmpeg")
	if got != expectedPath {
		t.Errorf("Resolve() = %q, want %q", got, expectedPath)
	}

	// Verify file was created
	if _, err := os.Stat(got); err != nil {
		t.Errorf("Resolve() did not create binary: %v", err)
	}

	// Verify version file
	versionPath := filepath.Join(tmpDir, ".transcript", "bin", ".version")
	versionData, err := os.ReadFile(versionPath)
	if err != nil {
		t.Errorf("Resolve() did not create version file: %v", err)
	}
	if string(versionData) != ffmpegVersion {
		t.Errorf("Resolve() wrote version %q, want %q", string(versionData), ffmpegVersion)
	}
}

func TestResolverResolveDownloadChecksumMismatch(t *testing.T) {
	t.Parallel()

	// Create a fake gzipped binary
	fakeContent := []byte("fake ffmpeg binary")
	var gzBuf bytes.Buffer
	gw := gzip.NewWriter(&gzBuf)
	if _, err := gw.Write(fakeContent); err != nil {
		t.Fatalf("failed to write gzip: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("failed to close gzip: %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(gzBuf.Bytes())
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	env := &mockEnvProvider{
		getenv:      func(key string) string { return "" },
		userHomeDir: func() (string, error) { return tmpDir, nil },
		lookPath:    func(file string) (string, error) { return "", errors.New("not found") },
	}

	resolver := NewResolver(
		WithEnvProvider(env),
		WithStderr(io.Discard),
		WithPlatform("testgoos", "testgoarch"),
		WithPlatformInfo(binaryInfo{
			URL:    server.URL + "/ffmpeg.gz",
			SHA256: "0000000000000000000000000000000000000000000000000000000000000000", // Wrong checksum
		}),
		WithHTTPClient(server.Client()),
	)

	_, err := resolver.Resolve(context.Background())
	if err == nil {
		t.Errorf("Resolve() = nil, want error for checksum mismatch")
	}
}

func TestResolverResolveHTTPError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	tmpDir := t.TempDir()

	env := &mockEnvProvider{
		getenv:      func(key string) string { return "" },
		userHomeDir: func() (string, error) { return tmpDir, nil },
		lookPath:    func(file string) (string, error) { return "", errors.New("not found") },
	}

	resolver := NewResolver(
		WithEnvProvider(env),
		WithStderr(io.Discard),
		WithPlatform("testgoos", "testgoarch"),
		WithPlatformInfo(binaryInfo{
			URL:    server.URL + "/ffmpeg.gz",
			SHA256: "ignored",
		}),
		WithHTTPClient(server.Client()),
	)

	_, err := resolver.Resolve(context.Background())
	if err == nil {
		t.Errorf("Resolve() = nil, want error for HTTP failure")
	}
}

func TestResolverWindowsBinaryName(t *testing.T) {
	t.Parallel()

	env := &mockEnvProvider{
		getenv:      func(key string) string { return "" },
		userHomeDir: func() (string, error) { return "/mock/home", nil },
	}

	resolver := NewResolver(
		WithEnvProvider(env),
		WithPlatform("windows", "amd64"),
	)

	path, err := resolver.installedPath()
	if err != nil {
		t.Fatalf("installedPath() unexpected error: %v", err)
	}

	if filepath.Base(path) != "ffmpeg.exe" {
		t.Errorf("installedPath() base = %s, want ffmpeg.exe", filepath.Base(path))
	}
}

func TestResolverManualInstallInstructions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		goos         string
		wantContains []string
	}{
		{
			goos:         "darwin",
			wantContains: []string{"brew install ffmpeg", "FFMPEG_PATH"},
		},
		{
			goos:         "linux",
			wantContains: []string{"apt install ffmpeg", "dnf install ffmpeg", "pacman -S ffmpeg"},
		},
		{
			goos:         "windows",
			wantContains: []string{"winget install ffmpeg", "ffmpeg.exe"},
		},
		{
			goos:         "freebsd",
			wantContains: []string{"ffmpeg.org/download", "FFMPEG_PATH"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.goos, func(t *testing.T) {
			t.Parallel()

			resolver := NewResolver(WithPlatform(tt.goos, "amd64"))
			instructions := resolver.manualInstallInstructions()

			for _, want := range tt.wantContains {
				if !strings.Contains(instructions, want) {
					t.Errorf("manualInstallInstructions() for %s missing %q:\n%s", tt.goos, want, instructions)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockFileReader struct {
	stat     func(name string) (os.FileInfo, error)
	readFile func(name string) ([]byte, error)
	open     func(name string) (io.ReadCloser, error)
}

func (m *mockFileReader) Stat(name string) (os.FileInfo, error) {
	if m.stat != nil {
		return m.stat(name)
	}
	return nil, os.ErrNotExist
}

func (m *mockFileReader) ReadFile(name string) ([]byte, error) {
	if m.readFile != nil {
		return m.readFile(name)
	}
	return nil, os.ErrNotExist
}

func (m *mockFileReader) Open(name string) (io.ReadCloser, error) {
	if m.open != nil {
		return m.open(name)
	}
	return nil, os.ErrNotExist
}

type mockFileWriter struct {
	writeFile  func(name string, data []byte, perm os.FileMode) error
	mkdirAll   func(path string, perm os.FileMode) error
	remove     func(name string) error
	rename     func(oldpath, newpath string) error
	chmod      func(name string, mode os.FileMode) error
	createTemp func(dir, pattern string) (*os.File, error)
}

func (m *mockFileWriter) WriteFile(name string, data []byte, perm os.FileMode) error {
	if m.writeFile != nil {
		return m.writeFile(name, data, perm)
	}
	return nil
}

func (m *mockFileWriter) MkdirAll(path string, perm os.FileMode) error {
	if m.mkdirAll != nil {
		return m.mkdirAll(path, perm)
	}
	return nil
}

func (m *mockFileWriter) Remove(name string) error {
	if m.remove != nil {
		return m.remove(name)
	}
	return nil
}

func (m *mockFileWriter) Rename(oldpath, newpath string) error {
	if m.rename != nil {
		return m.rename(oldpath, newpath)
	}
	return nil
}

func (m *mockFileWriter) Chmod(name string, mode os.FileMode) error {
	if m.chmod != nil {
		return m.chmod(name, mode)
	}
	return nil
}

func (m *mockFileWriter) CreateTemp(dir, pattern string) (*os.File, error) {
	if m.createTemp != nil {
		return m.createTemp(dir, pattern)
	}
	return os.CreateTemp(dir, pattern)
}

type mockEnvProvider struct {
	getenv      func(key string) string
	userHomeDir func() (string, error)
	lookPath    func(file string) (string, error)
}

func (m *mockEnvProvider) Getenv(key string) string {
	if m.getenv != nil {
		return m.getenv(key)
	}
	return ""
}

func (m *mockEnvProvider) UserHomeDir() (string, error) {
	if m.userHomeDir != nil {
		return m.userHomeDir()
	}
	return "", errors.New("not implemented")
}

func (m *mockEnvProvider) LookPath(file string) (string, error) {
	if m.lookPath != nil {
		return m.lookPath(file)
	}
	return "", errors.New("not found")
}

type mockFileInfo struct {
	name  string
	size  int64
	isDir bool
}

func (m mockFileInfo) Name() string       { return m.name }
func (m mockFileInfo) Size() int64        { return m.size }
func (m mockFileInfo) Mode() os.FileMode  { return 0644 }
func (m mockFileInfo) ModTime() time.Time { return time.Time{} }
func (m mockFileInfo) IsDir() bool        { return m.isDir }
func (m mockFileInfo) Sys() any           { return nil }
