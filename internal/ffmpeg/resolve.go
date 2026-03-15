package ffmpeg

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// FFmpeg version and download configuration.
// Binaries from github.com/eugeneware/ffmpeg-static release b6.1.1.
const (
	ffmpegVersion = "6.1.1"

	// binaryName is the base name of the ffmpeg binary.
	binaryName = "ffmpeg"

	// binaryExtWindows is the file extension for Windows executables.
	binaryExtWindows = ".exe"

	// downloadTimeout is the maximum time allowed for downloading ffmpeg.
	// Binary is ~20-30MB compressed, allowing for slow connections.
	downloadTimeout = 10 * time.Minute

	// versionFileName stores the installed version for upgrade detection.
	versionFileName = ".version"

	// minFFmpegMajorVersion is the minimum supported ffmpeg version.
	// Versions below this may lack required features (silencedetect improvements, codec support).
	minFFmpegMajorVersion = 4

	// installDirPerm is the permission mode for the FFmpeg install directory.
	installDirPerm = 0750

	// installDirName is the current FFmpeg install directory under home.
	installDirName = ".transcript"

	// legacyInstallDirName is the previous install directory (fallback read only).
	legacyInstallDirName = ".go-transcript"
)

// Environment variable for custom ffmpeg path.
const envFFmpegPath = "FFMPEG_PATH"

// defaultHTTPClient is a dedicated HTTP client for FFmpeg downloads with explicit timeouts.
var defaultHTTPClient = &http.Client{
	Timeout: downloadTimeout,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	},
}

// binaryInfo contains download metadata for ffmpeg.
type binaryInfo struct {
	URL    string // Download URL (gzipped binary)
	SHA256 string // Expected checksum of the gzipped file
}

// downloadBaseURL is the base URL for eugeneware/ffmpeg-static releases.
const downloadBaseURL = "https://github.com/eugeneware/ffmpeg-static/releases/download/b6.1.1"

// getPlatformInfo returns download information for the given platform.
// Returns the binaryInfo and true if the platform is supported, or zero value and false otherwise.
// This function encapsulates the platform map to avoid data races in concurrent access.
func getPlatformInfo(goos, goarch string) (binaryInfo, bool) {
	platforms := map[string]binaryInfo{
		"darwin-arm64": {
			URL:    downloadBaseURL + "/ffmpeg-darwin-arm64.gz",
			SHA256: "8923876afa8db5585022d7860ec7e589af192f441c56793971276d450ed3bbfa",
		},
		"darwin-amd64": {
			URL:    downloadBaseURL + "/ffmpeg-darwin-x64.gz",
			SHA256: "5d8fb6f280c428d0e82cd5ee68215f0734d64f88e37dcc9e082f818c9e5025f0",
		},
		"linux-amd64": {
			URL:    downloadBaseURL + "/ffmpeg-linux-x64.gz",
			SHA256: "bfe8a8fc511530457b528c48d77b5737527b504a3797a9bc4866aeca69c2dffa",
		},
		"windows-amd64": {
			URL:    downloadBaseURL + "/ffmpeg-win32-x64.gz",
			SHA256: "8883a3dffbd0a16cf4ef95206ea05283f78908dbfb118f73c83f4951dcc06d77",
		},
	}
	info, ok := platforms[goos+"-"+goarch]
	return info, ok
}

// ---------------------------------------------------------------------------
// Resolver - testable FFmpeg resolution with dependency injection
// ---------------------------------------------------------------------------

// Resolver finds and optionally downloads FFmpeg.
type Resolver struct {
	reader       fileReader
	writer       fileWriter
	http         httpDoer
	env          envProvider
	stderr       io.Writer
	goos         string
	goarch       string
	platformInfo *binaryInfo // Override for testing; nil uses getPlatformInfo
}

// ResolverOption configures a Resolver.
type ResolverOption func(*Resolver)

// WithFileReader sets the file reader implementation.
func WithFileReader(r fileReader) ResolverOption {
	return func(res *Resolver) { res.reader = r }
}

// WithFileWriter sets the file writer implementation.
func WithFileWriter(w fileWriter) ResolverOption {
	return func(res *Resolver) { res.writer = w }
}

// WithHTTPClient sets the HTTP client implementation.
func WithHTTPClient(c httpDoer) ResolverOption {
	return func(res *Resolver) { res.http = c }
}

// WithEnvProvider sets the environment provider implementation.
func WithEnvProvider(e envProvider) ResolverOption {
	return func(res *Resolver) { res.env = e }
}

// WithStderr sets the writer for status messages.
func WithStderr(w io.Writer) ResolverOption {
	return func(res *Resolver) { res.stderr = w }
}

// WithPlatform sets the target platform (for testing cross-platform behavior).
func WithPlatform(goos, goarch string) ResolverOption {
	return func(res *Resolver) {
		res.goos = goos
		res.goarch = goarch
	}
}

// WithPlatformInfo overrides the platform download info (for testing downloads).
func WithPlatformInfo(info binaryInfo) ResolverOption {
	return func(res *Resolver) {
		res.platformInfo = &info
	}
}

// NewResolver creates a Resolver with the given options.
// Uses production defaults if no options are provided.
func NewResolver(opts ...ResolverOption) *Resolver {
	r := &Resolver{
		reader: osFileReader{},
		writer: osFileWriter{},
		http:   defaultHTTPClient,
		env:    osEnvProvider{},
		stderr: os.Stderr,
		goos:   runtime.GOOS,
		goarch: runtime.GOARCH,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Resolve finds ffmpeg using the following precedence:
//  1. FFMPEG_PATH environment variable (error if set but invalid)
//  2. ~/.transcript/bin/ffmpeg (installed by us)
//  3. ~/.go-transcript/bin/ffmpeg (legacy install location, fallback)
//  4. System PATH
//  5. Auto-download if nothing found
func (r *Resolver) Resolve(ctx context.Context) (string, error) {
	// 1. Check FFMPEG_PATH environment variable
	if envPath := r.env.Getenv(envFFmpegPath); envPath != "" {
		if _, err := r.reader.Stat(envPath); err != nil {
			return "", fmt.Errorf("%w: %s is set to %q but binary not found (unset to enable auto-download)",
				ErrNotFound, envFFmpegPath, envPath)
		}
		return envPath, nil
	}

	// 2-3. Check install directories (new path first, then legacy fallback)
	installedPath, installed, err := r.findInstalledPath()
	if err != nil {
		return "", err
	}
	if installed {
		return installedPath, nil
	}

	// 4. Check system PATH
	if path, err := r.env.LookPath("ffmpeg"); err == nil {
		return path, nil
	}

	// 5. Auto-download
	fmt.Fprintln(r.stderr, "ffmpeg not found, downloading...")
	if err := r.downloadAndInstall(ctx); err != nil {
		return "", fmt.Errorf("%w: auto-download failed: %v\n\n%s",
			ErrNotFound, err, r.manualInstallInstructions())
	}

	path, _ := r.installedPath()
	return path, nil
}

// installDir returns the directory where ffmpeg is installed.
func (r *Resolver) installDir() (string, error) {
	home, err := r.env.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, installDirName, "bin"), nil
}

// legacyInstallDir returns the previous install directory used before rename.
func (r *Resolver) legacyInstallDir() (string, error) {
	home, err := r.env.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, legacyInstallDirName, "bin"), nil
}

// installedPath returns the path where ffmpeg would be installed.
func (r *Resolver) installedPath() (string, error) {
	dir, err := r.installDir()
	if err != nil {
		return "", err
	}
	return r.installedPathInDir(dir), nil
}

// installedPathInDir builds the ffmpeg binary path for the provided install directory.
func (r *Resolver) installedPathInDir(dir string) string {
	name := binaryName
	if r.goos == "windows" {
		name += binaryExtWindows
	}
	return filepath.Join(dir, name)
}

// findInstalledPath returns the first valid install path, preferring the new
// location and falling back to the legacy location.
func (r *Resolver) findInstalledPath() (string, bool, error) {
	newDir, err := r.installDir()
	if err != nil {
		return "", false, err
	}
	newPath := r.installedPathInDir(newDir)
	newVersionPath := filepath.Join(newDir, versionFileName)

	ok, err := r.isInstalledAt(newPath, newVersionPath)
	if err != nil {
		return "", false, err
	}
	if ok {
		return newPath, true, nil
	}

	legacyDir, err := r.legacyInstallDir()
	if err != nil {
		return "", false, err
	}
	legacyPath := r.installedPathInDir(legacyDir)
	legacyVersionPath := filepath.Join(legacyDir, versionFileName)

	ok, err = r.isInstalledAt(legacyPath, legacyVersionPath)
	if err != nil {
		return "", false, err
	}
	if ok {
		return legacyPath, true, nil
	}

	return "", false, nil
}

// isInstalledAt checks whether ffmpeg and matching version metadata exist.
// Note: There is a TOCTOU race between Stat and ReadFile, but this is acceptable
// because the worst case is a redundant download, which is idempotent.
func (r *Resolver) isInstalledAt(ffmpegPath, versionPath string) (bool, error) {
	if _, err := r.reader.Stat(ffmpegPath); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("cannot access ffmpeg binary %s: %w", ffmpegPath, err)
	}

	data, err := r.reader.ReadFile(versionPath)
	if err != nil {
		return false, nil // Version file missing = needs reinstall
	}
	if string(data) != ffmpegVersion {
		return false, nil // Version mismatch = needs upgrade
	}

	return true, nil
}

// downloadAndInstall downloads and installs ffmpeg.
func (r *Resolver) downloadAndInstall(ctx context.Context) error {
	var info binaryInfo
	if r.platformInfo != nil {
		info = *r.platformInfo
	} else {
		var ok bool
		info, ok = getPlatformInfo(r.goos, r.goarch)
		if !ok {
			return fmt.Errorf("%w: %s-%s (supported: darwin-arm64, darwin-amd64, linux-amd64, windows-amd64)",
				ErrUnsupportedPlatform, r.goos, r.goarch)
		}
	}

	dir, err := r.installDir()
	if err != nil {
		return err
	}

	// Create install directory
	if err := r.writer.MkdirAll(dir, installDirPerm); err != nil {
		return fmt.Errorf("cannot create install directory %s: %w", dir, err)
	}

	// Determine binary name
	name := binaryName
	if r.goos == "windows" {
		name += binaryExtWindows
	}
	destPath := filepath.Join(dir, name)

	// Download binary
	if err := r.downloadBinary(ctx, info, destPath); err != nil {
		_ = r.writer.Remove(destPath) // Cleanup on failure
		return fmt.Errorf("download ffmpeg: %w", err)
	}

	// Write version file
	versionPath := filepath.Join(dir, versionFileName)
	if err := r.writer.WriteFile(versionPath, []byte(ffmpegVersion), 0644); err != nil {
		return fmt.Errorf("write version file: %w", err)
	}

	return nil
}

// downloadBinary downloads, verifies, and extracts ffmpeg.
func (r *Resolver) downloadBinary(ctx context.Context, info binaryInfo, destPath string) error {
	dir := filepath.Dir(destPath)
	tempFile, err := r.writer.CreateTemp(dir, ".download-*")
	if err != nil {
		return fmt.Errorf("cannot create temp file: %w", err)
	}
	tempPath := tempFile.Name()
	tempFileClosed := false

	// Ensure cleanup on any error
	defer func() {
		if !tempFileClosed {
			_ = tempFile.Close()
		}
		_ = r.writer.Remove(tempPath)
	}()

	// Download - timeout is enforced by defaultHTTPClient.Timeout
	if err := r.downloadToFile(ctx, info.URL, tempFile); err != nil {
		return err
	}

	// Close to flush writes before checksum
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	tempFileClosed = true

	// Verify checksum
	if err := verifyChecksum(tempPath, info.SHA256); err != nil {
		return err
	}

	// Decompress gzip to final destination
	if err := decompressGzip(tempPath, destPath); err != nil {
		return err
	}

	// Make executable on Unix
	if r.goos != "windows" {
		if err := r.writer.Chmod(destPath, 0755); err != nil {
			return fmt.Errorf("make binary executable: %w", err)
		}
	}

	return nil
}

// downloadToFile downloads a URL to an open file.
func (r *Resolver) downloadToFile(ctx context.Context, url string, dest *os.File) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("%w: invalid URL: %v", ErrDownloadFailed, err)
	}

	resp, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w: HTTP %d from %s", ErrDownloadFailed, resp.StatusCode, url)
	}

	if _, err = io.Copy(dest, resp.Body); err != nil {
		return fmt.Errorf("%w: %v", ErrDownloadFailed, err)
	}

	return nil
}

// manualInstallInstructions returns platform-specific instructions.
func (r *Resolver) manualInstallInstructions() string {
	switch r.goos {
	case "darwin":
		return `To install FFmpeg manually:
  brew install ffmpeg

Or download from https://evermeet.cx/ffmpeg/

Or set FFMPEG_PATH environment variable to your ffmpeg binary.`
	case "linux":
		return `To install FFmpeg manually:
  Ubuntu/Debian: sudo apt install ffmpeg
  Fedora:        sudo dnf install ffmpeg
  Arch:          sudo pacman -S ffmpeg

Or set FFMPEG_PATH environment variable to your ffmpeg binary.`
	case "windows":
		return `To install FFmpeg manually:
  winget install ffmpeg

Or download from https://www.gyan.dev/ffmpeg/builds/

Or set FFMPEG_PATH environment variable to your ffmpeg.exe.`
	default:
		return `To install FFmpeg manually, download from https://ffmpeg.org/download.html
Or set FFMPEG_PATH environment variable to your ffmpeg binary.`
	}
}

// ---------------------------------------------------------------------------
// Package-level functions - backward compatible facade
// ---------------------------------------------------------------------------

var (
	defaultResolver     *Resolver
	defaultResolverOnce sync.Once
)

// getDefaultResolver returns the lazily-initialized default resolver.
func getDefaultResolver() *Resolver {
	defaultResolverOnce.Do(func() {
		defaultResolver = NewResolver()
	})
	return defaultResolver
}

// Resolve finds ffmpeg using the default resolver.
// This is a backward-compatible facade for the Resolver.Resolve method.
func Resolve(ctx context.Context) (string, error) {
	return getDefaultResolver().Resolve(ctx)
}

// VersionChecker verifies FFmpeg version requirements.
type VersionChecker struct {
	executor *Executor
	stderr   io.Writer
}

// VersionCheckerOption configures a VersionChecker.
type VersionCheckerOption func(*VersionChecker)

// WithVersionExecutor sets the executor for running FFmpeg.
func WithVersionExecutor(e *Executor) VersionCheckerOption {
	return func(vc *VersionChecker) { vc.executor = e }
}

// WithVersionStderr sets the writer for warning messages.
func WithVersionStderr(w io.Writer) VersionCheckerOption {
	return func(vc *VersionChecker) { vc.stderr = w }
}

// NewVersionChecker creates a VersionChecker with the given options.
func NewVersionChecker(opts ...VersionCheckerOption) *VersionChecker {
	vc := &VersionChecker{
		executor: getDefaultExecutor(),
		stderr:   os.Stderr,
	}
	for _, opt := range opts {
		opt(vc)
	}
	return vc
}

// Check verifies that ffmpeg meets minimum version requirements.
// Prints a warning to stderr if version is below minimum but doesn't fail.
// Returns true if version was successfully checked, false if parsing failed.
func (vc *VersionChecker) Check(ctx context.Context, ffmpegPath string) bool {
	output, err := vc.executor.RunOutput(ctx, ffmpegPath, []string{"-version"})
	if err != nil && output == "" {
		return false // Can't check version, proceed anyway
	}

	// Parse version from output like "ffmpeg version 6.1.1 Copyright..."
	lines := strings.Split(output, "\n")
	if len(lines) == 0 || lines[0] == "" {
		return false
	}

	var major int
	_, err = fmt.Sscanf(lines[0], "ffmpeg version %d", &major)
	if err != nil {
		// Try alternative format "ffmpeg version n6.1.1..."
		_, err = fmt.Sscanf(lines[0], "ffmpeg version n%d", &major)
		if err != nil {
			return false // Can't parse version
		}
	}

	if major < minFFmpegMajorVersion {
		fmt.Fprintf(vc.stderr, "Warning: ffmpeg version %d detected, version %d+ recommended\n",
			major, minFFmpegMajorVersion)
	}
	return true
}

// CheckVersion verifies that ffmpeg meets minimum version requirements.
// This is a backward-compatible facade for the VersionChecker.Check method.
func CheckVersion(ctx context.Context, ffmpegPath string) {
	NewVersionChecker().Check(ctx, ffmpegPath)
}

// ---------------------------------------------------------------------------
// Pure helper functions
// These functions use os package directly rather than injected dependencies.
// This is intentional: they operate on internal temp files only and keeping
// them as pure functions simplifies the code without sacrificing testability
// (they are tested directly with t.TempDir).
// ---------------------------------------------------------------------------

// verifyChecksum computes the SHA256 of a file and compares to expected.
func verifyChecksum(filePath, expectedSHA256 string) error {
	f, err := os.Open(filePath) // #nosec G304 -- filePath is internal temp file
	if err != nil {
		return fmt.Errorf("cannot open file for checksum: %w", err)
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("compute checksum: %w", err)
	}

	actual := hex.EncodeToString(h.Sum(nil))
	if actual != expectedSHA256 {
		return fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, expectedSHA256, actual)
	}

	return nil
}

// decompressGzip decompresses a gzip file to a destination path.
// Uses atomic write pattern with size limit to prevent decompression bombs.
// FFmpeg binary is ~80MB uncompressed; 200MB limit provides safety margin.
const maxDecompressedSize = 200 * 1024 * 1024

func decompressGzip(gzPath, destPath string) error {
	gzFile, err := os.Open(gzPath) // #nosec G304 -- gzPath is internal temp file
	if err != nil {
		return fmt.Errorf("cannot open gzip file: %w", err)
	}
	defer func() { _ = gzFile.Close() }()

	gzReader, err := gzip.NewReader(gzFile)
	if err != nil {
		return fmt.Errorf("invalid gzip file: %w", err)
	}
	defer func() { _ = gzReader.Close() }()

	// Create temp file for atomic write
	dir := filepath.Dir(destPath)
	tempFile, err := os.CreateTemp(dir, ".extract-*")
	if err != nil {
		return fmt.Errorf("cannot create temp file: %w", err)
	}
	tempPath := tempFile.Name()

	// Ensure cleanup on error
	success := false
	defer func() {
		_ = tempFile.Close()
		if !success {
			_ = os.Remove(tempPath)
		}
	}()

	// Decompress with size limit to prevent decompression bombs
	limitedReader := io.LimitReader(gzReader, maxDecompressedSize)
	written, err := io.Copy(tempFile, limitedReader)
	if err != nil {
		return fmt.Errorf("decompression failed: %w", err)
	}
	if written >= maxDecompressedSize {
		return fmt.Errorf("decompression failed: file exceeds %d bytes limit", maxDecompressedSize)
	}

	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}

	// Atomic rename
	if err := os.Rename(tempPath, destPath); err != nil {
		return fmt.Errorf("install binary: %w", err)
	}

	success = true
	return nil
}
