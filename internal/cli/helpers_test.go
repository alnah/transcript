package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/config"
)

// ---------------------------------------------------------------------------
// syncBuffer - thread-safe bytes.Buffer for concurrent test output
// ---------------------------------------------------------------------------

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

// Compile-time check that syncBuffer implements io.Writer.
var _ io.Writer = (*syncBuffer)(nil)

// ---------------------------------------------------------------------------
// testMocks - convenience struct for grouping all mocks
// ---------------------------------------------------------------------------

type testMocks struct {
	ffmpegResolver *mockFFmpegResolver
	configLoader   *mockConfigLoader
	transcriber    *mockTranscriberFactory
	restructurer   *mockRestructurerFactory
	chunker        *mockChunkerFactory
	recorder       *mockRecorderFactory
	deviceLister   *mockDeviceListerFactory
}

func newTestMocks() *testMocks {
	return &testMocks{
		ffmpegResolver: &mockFFmpegResolver{},
		configLoader:   &mockConfigLoader{},
		transcriber:    &mockTranscriberFactory{},
		restructurer:   &mockRestructurerFactory{},
		chunker:        &mockChunkerFactory{},
		recorder:       &mockRecorderFactory{},
		deviceLister:   &mockDeviceListerFactory{},
	}
}

// ---------------------------------------------------------------------------
// testEnv - creates a fully mocked Env for testing
// ---------------------------------------------------------------------------

// testEnvOptions configures a test environment.
type testEnvOptions struct {
	stderr io.Writer
	getenv func(string) string
	now    func() time.Time
	mocks  *testMocks
}

// testEnvOption configures testEnv.
type testEnvOption func(*testEnvOptions)

// testEnv creates a test Env with all dependencies mocked.
// Returns the Env and the mocks for assertions.
func testEnv(opts ...testEnvOption) (*Env, *testMocks) {
	options := &testEnvOptions{
		stderr: &syncBuffer{},
		getenv: defaultTestEnv,
		now: func() time.Time {
			return time.Date(2026, 1, 26, 14, 30, 52, 0, time.UTC)
		},
		mocks: newTestMocks(),
	}

	for _, opt := range opts {
		opt(options)
	}

	env := &Env{
		Stderr:              options.stderr,
		Getenv:              options.getenv,
		Now:                 options.now,
		FFmpegResolver:      options.mocks.ffmpegResolver,
		ConfigLoader:        options.mocks.configLoader,
		TranscriberFactory:  options.mocks.transcriber,
		RestructurerFactory: options.mocks.restructurer,
		ChunkerFactory:      options.mocks.chunker,
		RecorderFactory:     options.mocks.recorder,
		DeviceListerFactory: options.mocks.deviceLister,
	}

	return env, options.mocks
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// fixedTime returns a function that always returns the given time.
func fixedTime(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// staticEnv returns a getenv function that returns values from the given map.
func staticEnv(env map[string]string) func(string) string {
	return func(key string) string {
		return env[key]
	}
}

// defaultTestEnv returns API keys for both OpenAI and DeepSeek.
func defaultTestEnv(key string) string {
	switch key {
	case EnvOpenAIAPIKey:
		return "test-openai-key"
	case EnvDeepSeekAPIKey:
		return "test-deepseek-key"
	default:
		return ""
	}
}

// createTestAudioFile creates a temporary audio file for testing.
// Returns the file path. The file is automatically cleaned up after the test.
func createTestAudioFile(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)

	// Write minimal content to make the file non-empty
	if err := os.WriteFile(path, []byte("fake audio content"), 0644); err != nil {
		t.Fatalf("failed to create test audio file: %v", err)
	}
	return path
}

// configWithOutputDir returns a ConfigLoader that returns a config with the given output directory.
func configWithOutputDir(outputDir string) *mockConfigLoader {
	return &mockConfigLoader{
		LoadFunc: func() (config.Config, error) {
			return config.Config{OutputDir: outputDir}, nil
		},
	}
}

// ---------------------------------------------------------------------------
// Extension warning test helpers
// ---------------------------------------------------------------------------

// transcribeEnvForExtensionTest creates an Env configured for testing extension warnings.
// Returns the Env and a function to get stderr output.
func transcribeEnvForExtensionTest(t *testing.T) (*Env, func() string) {
	t.Helper()

	chunkPath := filepath.Join(t.TempDir(), "chunk_0.ogg")
	if err := os.WriteFile(chunkPath, []byte("chunk"), 0644); err != nil {
		t.Fatalf("failed to create chunk: %v", err)
	}

	stderr := &syncBuffer{}
	env := &Env{
		Stderr:         stderr,
		Getenv:         defaultTestEnv,
		Now:            fixedTime(time.Now()),
		FFmpegResolver: &mockFFmpegResolver{},
		ConfigLoader:   &mockConfigLoader{},
		ChunkerFactory: &mockChunkerFactory{
			mockChunker: &mockChunker{
				ChunkFunc: func(ctx context.Context, audioPath string) ([]audio.Chunk, error) {
					return []audio.Chunk{{Path: chunkPath, Index: 0}}, nil
				},
			},
		},
		TranscriberFactory: &mockTranscriberFactory{},
	}

	return env, stderr.String
}

// structureEnvForExtensionTest creates an Env configured for testing extension warnings.
// Returns the Env and a function to get stderr output.
func structureEnvForExtensionTest(t *testing.T) (*Env, func() string) {
	t.Helper()

	stderr := &syncBuffer{}
	env := &Env{
		Stderr:       stderr,
		Getenv:       defaultTestEnv,
		ConfigLoader: &mockConfigLoader{},
		RestructurerFactory: &mockRestructurerFactory{
			mockMapReducer: &mockMapReduceRestructurer{},
		},
	}

	return env, stderr.String
}

// liveEnvForExtensionTest creates an Env configured for testing extension warnings.
// Returns the Env and a function to get stderr output.
func liveEnvForExtensionTest(t *testing.T, outputDir string) (*Env, func() string) {
	t.Helper()

	chunkPath := filepath.Join(outputDir, "chunk_0.ogg")
	if err := os.WriteFile(chunkPath, []byte("chunk"), 0644); err != nil {
		t.Fatalf("failed to create chunk: %v", err)
	}

	stderr := &syncBuffer{}
	env := &Env{
		Stderr:         stderr,
		Getenv:         defaultTestEnv,
		Now:            fixedTime(time.Now()),
		FFmpegResolver: &mockFFmpegResolver{},
		ConfigLoader:   &mockConfigLoader{},
		RecorderFactory: &mockRecorderFactory{
			mockRecorder: &mockRecorder{
				RecordFunc: func(ctx context.Context, duration time.Duration, output string) error {
					return os.WriteFile(output, []byte("recorded audio"), 0644)
				},
			},
		},
		ChunkerFactory: &mockChunkerFactory{
			mockChunker: &mockChunker{
				ChunkFunc: func(ctx context.Context, audioPath string) ([]audio.Chunk, error) {
					return []audio.Chunk{{Path: chunkPath, Index: 0}}, nil
				},
			},
		},
		TranscriberFactory: &mockTranscriberFactory{},
	}

	return env, stderr.String
}
