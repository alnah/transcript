package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/config"
	"github.com/alnah/transcript/internal/ffmpeg"
	"github.com/alnah/transcript/internal/restructure"
	"github.com/alnah/transcript/internal/transcribe"
)

// Env holds injectable dependencies for CLI commands.
// This is the central injection point for testing CLI commands in isolation.
//
// All fields have sensible defaults via DefaultEnv(). Tests can override
// specific fields using the With* options or by creating a custom Env.
//
// Env must not be nil when passed to command functions. Use DefaultEnv()
// or NewEnv() to create a valid instance.
type Env struct {
	// I/O and environment
	Stderr io.Writer
	Getenv func(string) string
	Now    func() time.Time

	// Factories for domain objects
	FFmpegResolver      FFmpegResolver
	ConfigLoader        ConfigLoader
	TranscriberFactory  TranscriberFactory
	RestructurerFactory RestructurerFactory
	ChunkerFactory      ChunkerFactory
	RecorderFactory     RecorderFactory
	DeviceListerFactory DeviceListerFactory
}

// FFmpegResolver resolves the path to the FFmpeg binary.
type FFmpegResolver interface {
	Resolve(ctx context.Context) (string, error)
	CheckVersion(ctx context.Context, ffmpegPath string)
}

// ConfigLoader loads and provides access to configuration.
type ConfigLoader interface {
	Load() (config.Config, error)
}

// TranscriberFactory creates transcribers for audio-to-text conversion.
type TranscriberFactory interface {
	NewTranscriber(apiKey string) transcribe.Transcriber
}

// Restructuring provider constants.
const (
	// ProviderDeepSeek uses DeepSeek API for restructuring.
	ProviderDeepSeek = "deepseek"
	// ProviderOpenAI uses OpenAI API for restructuring.
	ProviderOpenAI = "openai"
)

// RestructurerFactory creates restructurers for transcript formatting.
type RestructurerFactory interface {
	// NewMapReducer creates a MapReducer configured with the given provider, API key, and options.
	// Provider must be a valid Provider (DeepSeekProvider or OpenAIProvider).
	// This is the primary method for creating restructurers in CLI commands.
	NewMapReducer(provider Provider, apiKey string, opts ...restructure.MapReduceOption) (restructure.MapReducer, error)
}

// ChunkerFactory creates audio chunkers.
type ChunkerFactory interface {
	NewSilenceChunker(ffmpegPath string) (audio.Chunker, error)
}

// RecorderFactory creates audio recorders.
type RecorderFactory interface {
	NewRecorder(ffmpegPath, device string) (audio.Recorder, error)
	NewLoopbackRecorder(ctx context.Context, ffmpegPath string) (audio.Recorder, error)
	NewMixRecorder(ctx context.Context, ffmpegPath, micDevice string) (audio.Recorder, error)
}

// DeviceListerFactory creates device listers for audio device discovery.
type DeviceListerFactory interface {
	NewDeviceLister(ffmpegPath string) (audio.DeviceLister, error)
}

// EnvOption configures an Env.
type EnvOption func(*Env)

// WithStderr sets the stderr writer.
func WithStderr(w io.Writer) EnvOption {
	return func(e *Env) {
		e.Stderr = w
	}
}

// WithGetenv sets the environment variable getter.
func WithGetenv(fn func(string) string) EnvOption {
	return func(e *Env) {
		e.Getenv = fn
	}
}

// WithNow sets the time provider.
func WithNow(fn func() time.Time) EnvOption {
	return func(e *Env) {
		e.Now = fn
	}
}

// WithFFmpegResolver sets the FFmpeg resolver.
func WithFFmpegResolver(r FFmpegResolver) EnvOption {
	return func(e *Env) {
		e.FFmpegResolver = r
	}
}

// WithConfigLoader sets the config loader.
func WithConfigLoader(l ConfigLoader) EnvOption {
	return func(e *Env) {
		e.ConfigLoader = l
	}
}

// WithTranscriberFactory sets the transcriber factory.
func WithTranscriberFactory(f TranscriberFactory) EnvOption {
	return func(e *Env) {
		e.TranscriberFactory = f
	}
}

// WithRestructurerFactory sets the restructurer factory.
func WithRestructurerFactory(f RestructurerFactory) EnvOption {
	return func(e *Env) {
		e.RestructurerFactory = f
	}
}

// WithChunkerFactory sets the chunker factory.
func WithChunkerFactory(f ChunkerFactory) EnvOption {
	return func(e *Env) {
		e.ChunkerFactory = f
	}
}

// WithRecorderFactory sets the recorder factory.
func WithRecorderFactory(f RecorderFactory) EnvOption {
	return func(e *Env) {
		e.RecorderFactory = f
	}
}

// WithDeviceListerFactory sets the device lister factory.
func WithDeviceListerFactory(f DeviceListerFactory) EnvOption {
	return func(e *Env) {
		e.DeviceListerFactory = f
	}
}

// DefaultEnv returns an Env with production defaults.
func DefaultEnv() *Env {
	return &Env{
		Stderr:              os.Stderr,
		Getenv:              os.Getenv,
		Now:                 time.Now,
		FFmpegResolver:      &defaultFFmpegResolver{},
		ConfigLoader:        &defaultConfigLoader{},
		TranscriberFactory:  &defaultTranscriberFactory{},
		RestructurerFactory: &defaultRestructurerFactory{},
		ChunkerFactory:      &defaultChunkerFactory{},
		RecorderFactory:     &defaultRecorderFactory{},
		DeviceListerFactory: &defaultDeviceListerFactory{},
	}
}

// NewEnv creates an Env with the given options applied to defaults.
func NewEnv(opts ...EnvOption) *Env {
	env := DefaultEnv()
	for _, opt := range opts {
		opt(env)
	}
	return env
}

// ---------------------------------------------------------------------------
// Default implementations - delegate to real packages
// ---------------------------------------------------------------------------

// defaultFFmpegResolver implements FFmpegResolver using the ffmpeg package.
type defaultFFmpegResolver struct{}

func (defaultFFmpegResolver) Resolve(ctx context.Context) (string, error) {
	return ffmpeg.Resolve(ctx)
}

func (defaultFFmpegResolver) CheckVersion(ctx context.Context, ffmpegPath string) {
	ffmpeg.CheckVersion(ctx, ffmpegPath)
}

// defaultConfigLoader implements ConfigLoader using the config package.
type defaultConfigLoader struct{}

func (defaultConfigLoader) Load() (config.Config, error) {
	return config.Load()
}

// defaultTranscriberFactory implements TranscriberFactory using OpenAI.
type defaultTranscriberFactory struct{}

func (defaultTranscriberFactory) NewTranscriber(apiKey string) transcribe.Transcriber {
	return transcribe.NewOpenAITranscriber(apiKey)
}

// defaultRestructurerFactory implements RestructurerFactory with provider selection.
type defaultRestructurerFactory struct{}

// ErrUnsupportedProvider indicates an unknown provider was passed to the factory.
// With the Provider type, this error is only reachable if:
// 1. A zero-value Provider is passed without defaulting
// 2. The Provider type is extended but the factory is not updated
// Normal CLI flows default zero providers to DeepSeek before calling the factory.
var ErrUnsupportedProvider = fmt.Errorf("unsupported provider (use %q or %q)", ProviderDeepSeek, ProviderOpenAI)

func (defaultRestructurerFactory) NewMapReducer(provider Provider, apiKey string, opts ...restructure.MapReduceOption) (restructure.MapReducer, error) {
	switch {
	case provider.IsDeepSeek():
		restructurer, err := restructure.NewDeepSeekRestructurer(apiKey)
		if err != nil {
			return nil, err
		}
		return restructure.NewMapReduceRestructurer(restructurer, opts...), nil
	case provider.IsOpenAI():
		restructurer := restructure.NewOpenAIRestructurer(apiKey)
		return restructure.NewMapReduceRestructurer(restructurer, opts...), nil
	default:
		// Defensive: Provider type guarantees validity, but handle zero value
		// or future provider additions gracefully.
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedProvider, provider)
	}
}

// defaultChunkerFactory implements ChunkerFactory using audio package.
type defaultChunkerFactory struct{}

func (defaultChunkerFactory) NewSilenceChunker(ffmpegPath string) (audio.Chunker, error) {
	return audio.NewSilenceChunker(ffmpegPath)
}

// defaultDeviceListerFactory implements DeviceListerFactory using audio package.
type defaultDeviceListerFactory struct{}

func (defaultDeviceListerFactory) NewDeviceLister(ffmpegPath string) (audio.DeviceLister, error) {
	return audio.NewFFmpegRecorder(ffmpegPath, "")
}

// defaultRecorderFactory implements RecorderFactory using audio package.
type defaultRecorderFactory struct{}

func (defaultRecorderFactory) NewRecorder(ffmpegPath, device string) (audio.Recorder, error) {
	return audio.NewFFmpegRecorder(ffmpegPath, device)
}

func (defaultRecorderFactory) NewLoopbackRecorder(ctx context.Context, ffmpegPath string) (audio.Recorder, error) {
	return audio.NewFFmpegLoopbackRecorder(ctx, ffmpegPath)
}

func (defaultRecorderFactory) NewMixRecorder(ctx context.Context, ffmpegPath, micDevice string) (audio.Recorder, error) {
	return audio.NewFFmpegMixRecorder(ctx, ffmpegPath, micDevice)
}

// Compile-time interface verification.
var (
	_ FFmpegResolver      = (*defaultFFmpegResolver)(nil)
	_ ConfigLoader        = (*defaultConfigLoader)(nil)
	_ TranscriberFactory  = (*defaultTranscriberFactory)(nil)
	_ RestructurerFactory = (*defaultRestructurerFactory)(nil)
	_ ChunkerFactory      = (*defaultChunkerFactory)(nil)
	_ RecorderFactory     = (*defaultRecorderFactory)(nil)
	_ DeviceListerFactory = (*defaultDeviceListerFactory)(nil)
)
