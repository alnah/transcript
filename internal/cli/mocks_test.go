package cli

import (
	"context"
	"sync"
	"time"

	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/config"
	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/restructure"
	"github.com/alnah/transcript/internal/template"
	"github.com/alnah/transcript/internal/transcribe"
)

// ---------------------------------------------------------------------------
// Mock FFmpegResolver
// ---------------------------------------------------------------------------

type mockFFmpegResolver struct {
	ResolveFunc      func(ctx context.Context) (string, error)
	CheckVersionFunc func(ctx context.Context, ffmpegPath string)

	mu           sync.Mutex
	resolveCalls int
}

func (m *mockFFmpegResolver) Resolve(ctx context.Context) (string, error) {
	m.mu.Lock()
	m.resolveCalls++
	m.mu.Unlock()

	if m.ResolveFunc != nil {
		return m.ResolveFunc(ctx)
	}
	return "/usr/bin/ffmpeg", nil
}

func (m *mockFFmpegResolver) CheckVersion(ctx context.Context, ffmpegPath string) {
	if m.CheckVersionFunc != nil {
		m.CheckVersionFunc(ctx, ffmpegPath)
	}
}

func (m *mockFFmpegResolver) ResolveCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolveCalls
}

// ---------------------------------------------------------------------------
// Mock ConfigLoader
// ---------------------------------------------------------------------------

type mockConfigLoader struct {
	LoadFunc func() (config.Config, error)

	mu        sync.Mutex
	loadCalls int
}

func (m *mockConfigLoader) Load() (config.Config, error) {
	m.mu.Lock()
	m.loadCalls++
	m.mu.Unlock()

	if m.LoadFunc != nil {
		return m.LoadFunc()
	}
	return config.Config{}, nil
}

func (m *mockConfigLoader) LoadCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.loadCalls
}

// ---------------------------------------------------------------------------
// Mock TranscriberFactory + Transcriber
// ---------------------------------------------------------------------------

type mockTranscriberFactory struct {
	NewTranscriberFunc func(apiKey string) transcribe.Transcriber

	mu                  sync.Mutex
	newTranscriberCalls []string // API keys passed
}

func (m *mockTranscriberFactory) NewTranscriber(apiKey string) transcribe.Transcriber {
	m.mu.Lock()
	m.newTranscriberCalls = append(m.newTranscriberCalls, apiKey)
	m.mu.Unlock()

	if m.NewTranscriberFunc != nil {
		return m.NewTranscriberFunc(apiKey)
	}
	return &mockTranscriber{}
}

func (m *mockTranscriberFactory) NewTranscriberCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.newTranscriberCalls...)
}

type mockTranscriber struct {
	TranscribeFunc func(ctx context.Context, audioPath string, opts transcribe.Options) (string, error)

	mu              sync.Mutex
	transcribeCalls []transcribeCall
}

type transcribeCall struct {
	AudioPath string
	Opts      transcribe.Options
}

func (m *mockTranscriber) Transcribe(ctx context.Context, audioPath string, opts transcribe.Options) (string, error) {
	m.mu.Lock()
	m.transcribeCalls = append(m.transcribeCalls, transcribeCall{AudioPath: audioPath, Opts: opts})
	m.mu.Unlock()

	if m.TranscribeFunc != nil {
		return m.TranscribeFunc(ctx, audioPath, opts)
	}
	return "transcribed text", nil
}

func (m *mockTranscriber) TranscribeCalls() []transcribeCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]transcribeCall, len(m.transcribeCalls))
	copy(result, m.transcribeCalls)
	return result
}

// ---------------------------------------------------------------------------
// Mock RestructurerFactory
// ---------------------------------------------------------------------------

type mockRestructurerFactory struct {
	NewMapReducerFunc func(provider Provider, apiKey string, opts ...restructure.MapReduceOption) (restructure.MapReducer, error)
	NewMapReducerErr  error // Error to return from NewMapReducer

	mu                 sync.Mutex
	newMapReducerCalls []mapReducerCall
	mockMapReducer     *mockMapReduceRestructurer
}

type mapReducerCall struct {
	Provider Provider
	APIKey   string
}

func (m *mockRestructurerFactory) NewMapReducer(provider Provider, apiKey string, opts ...restructure.MapReduceOption) (restructure.MapReducer, error) {
	m.mu.Lock()
	m.newMapReducerCalls = append(m.newMapReducerCalls, mapReducerCall{Provider: provider, APIKey: apiKey})
	m.mu.Unlock()

	if m.NewMapReducerErr != nil {
		return nil, m.NewMapReducerErr
	}
	if m.NewMapReducerFunc != nil {
		return m.NewMapReducerFunc(provider, apiKey, opts...)
	}
	if m.mockMapReducer != nil {
		return m.mockMapReducer, nil
	}
	return &mockMapReduceRestructurer{}, nil
}

func (m *mockRestructurerFactory) NewMapReducerCalls() []mapReducerCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mapReducerCall, len(m.newMapReducerCalls))
	copy(result, m.newMapReducerCalls)
	return result
}

// ---------------------------------------------------------------------------
// Mock ChunkerFactory + Chunker
// ---------------------------------------------------------------------------

type mockChunkerFactory struct {
	NewSilenceChunkerFunc func(ffmpegPath string) (audio.Chunker, error)

	mu                     sync.Mutex
	newSilenceChunkerCalls []string
	mockChunker            *mockChunker
}

func (m *mockChunkerFactory) NewSilenceChunker(ffmpegPath string) (audio.Chunker, error) {
	m.mu.Lock()
	m.newSilenceChunkerCalls = append(m.newSilenceChunkerCalls, ffmpegPath)
	m.mu.Unlock()

	if m.NewSilenceChunkerFunc != nil {
		return m.NewSilenceChunkerFunc(ffmpegPath)
	}
	if m.mockChunker != nil {
		return m.mockChunker, nil
	}
	return &mockChunker{}, nil
}

func (m *mockChunkerFactory) NewSilenceChunkerCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.newSilenceChunkerCalls...)
}

type mockChunker struct {
	ChunkFunc func(ctx context.Context, audioPath string) ([]audio.Chunk, error)

	mu         sync.Mutex
	chunkCalls []string
}

func (m *mockChunker) Chunk(ctx context.Context, audioPath string) ([]audio.Chunk, error) {
	m.mu.Lock()
	m.chunkCalls = append(m.chunkCalls, audioPath)
	m.mu.Unlock()

	if m.ChunkFunc != nil {
		return m.ChunkFunc(ctx, audioPath)
	}
	// Return a single chunk by default
	return []audio.Chunk{
		{
			Path:      audioPath,
			Index:     0,
			StartTime: 0,
			EndTime:   5 * time.Minute,
		},
	}, nil
}

func (m *mockChunker) ChunkCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.chunkCalls...)
}

// ---------------------------------------------------------------------------
// Mock RecorderFactory + Recorder
// ---------------------------------------------------------------------------

type mockRecorderFactory struct {
	NewRecorderFunc         func(ffmpegPath, device string) (audio.Recorder, error)
	NewLoopbackRecorderFunc func(ctx context.Context, ffmpegPath string) (audio.Recorder, error)
	NewMixRecorderFunc      func(ctx context.Context, ffmpegPath, micDevice string) (audio.Recorder, error)

	mu                       sync.Mutex
	newRecorderCalls         []recorderCall
	newLoopbackRecorderCalls []string
	newMixRecorderCalls      []mixRecorderCall
	mockRecorder             *mockRecorder
}

type recorderCall struct {
	FFmpegPath string
	Device     string
}

type mixRecorderCall struct {
	FFmpegPath string
	MicDevice  string
}

func (m *mockRecorderFactory) NewRecorder(ffmpegPath, device string) (audio.Recorder, error) {
	m.mu.Lock()
	m.newRecorderCalls = append(m.newRecorderCalls, recorderCall{FFmpegPath: ffmpegPath, Device: device})
	m.mu.Unlock()

	if m.NewRecorderFunc != nil {
		return m.NewRecorderFunc(ffmpegPath, device)
	}
	if m.mockRecorder != nil {
		return m.mockRecorder, nil
	}
	return &mockRecorder{}, nil
}

func (m *mockRecorderFactory) NewLoopbackRecorder(ctx context.Context, ffmpegPath string) (audio.Recorder, error) {
	m.mu.Lock()
	m.newLoopbackRecorderCalls = append(m.newLoopbackRecorderCalls, ffmpegPath)
	m.mu.Unlock()

	if m.NewLoopbackRecorderFunc != nil {
		return m.NewLoopbackRecorderFunc(ctx, ffmpegPath)
	}
	if m.mockRecorder != nil {
		return m.mockRecorder, nil
	}
	return &mockRecorder{}, nil
}

func (m *mockRecorderFactory) NewMixRecorder(ctx context.Context, ffmpegPath, micDevice string) (audio.Recorder, error) {
	m.mu.Lock()
	m.newMixRecorderCalls = append(m.newMixRecorderCalls, mixRecorderCall{FFmpegPath: ffmpegPath, MicDevice: micDevice})
	m.mu.Unlock()

	if m.NewMixRecorderFunc != nil {
		return m.NewMixRecorderFunc(ctx, ffmpegPath, micDevice)
	}
	if m.mockRecorder != nil {
		return m.mockRecorder, nil
	}
	return &mockRecorder{}, nil
}

func (m *mockRecorderFactory) NewRecorderCalls() []recorderCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]recorderCall, len(m.newRecorderCalls))
	copy(result, m.newRecorderCalls)
	return result
}

func (m *mockRecorderFactory) NewLoopbackRecorderCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.newLoopbackRecorderCalls...)
}

func (m *mockRecorderFactory) NewMixRecorderCalls() []mixRecorderCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mixRecorderCall, len(m.newMixRecorderCalls))
	copy(result, m.newMixRecorderCalls)
	return result
}

type mockRecorder struct {
	RecordFunc func(ctx context.Context, duration time.Duration, output string) error

	mu          sync.Mutex
	recordCalls []recordCall
}

type recordCall struct {
	Duration time.Duration
	Output   string
}

func (m *mockRecorder) Record(ctx context.Context, duration time.Duration, output string) error {
	m.mu.Lock()
	m.recordCalls = append(m.recordCalls, recordCall{Duration: duration, Output: output})
	m.mu.Unlock()

	if m.RecordFunc != nil {
		return m.RecordFunc(ctx, duration, output)
	}
	return nil
}

func (m *mockRecorder) RecordCalls() []recordCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]recordCall, len(m.recordCalls))
	copy(result, m.recordCalls)
	return result
}

// ---------------------------------------------------------------------------
// Mock MapReduceRestructurer for testing restructure path
// ---------------------------------------------------------------------------

type mockMapReduceRestructurer struct {
	RestructureFunc func(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error)

	mu               sync.Mutex
	restructureCalls []mapReduceRestructureCall
}

type mapReduceRestructureCall struct {
	Transcript   string
	TemplateName template.Name
	OutputLang   lang.Language
}

func (m *mockMapReduceRestructurer) Restructure(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, bool, error) {
	m.mu.Lock()
	m.restructureCalls = append(m.restructureCalls, mapReduceRestructureCall{
		Transcript:   transcript,
		TemplateName: tmpl,
		OutputLang:   outputLang,
	})
	m.mu.Unlock()

	if m.RestructureFunc != nil {
		return m.RestructureFunc(ctx, transcript, tmpl, outputLang)
	}
	return "restructured text", false, nil
}

func (m *mockMapReduceRestructurer) RestructureCalls() []mapReduceRestructureCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]mapReduceRestructureCall, len(m.restructureCalls))
	copy(result, m.restructureCalls)
	return result
}

// ---------------------------------------------------------------------------
// Mock DeviceListerFactory + DeviceLister
// ---------------------------------------------------------------------------

type mockDeviceListerFactory struct {
	NewDeviceListerFunc func(ffmpegPath string) (audio.DeviceLister, error)

	mu                   sync.Mutex
	newDeviceListerCalls []string
	mockDeviceLister     *mockDeviceLister
}

func (m *mockDeviceListerFactory) NewDeviceLister(ffmpegPath string) (audio.DeviceLister, error) {
	m.mu.Lock()
	m.newDeviceListerCalls = append(m.newDeviceListerCalls, ffmpegPath)
	m.mu.Unlock()

	if m.NewDeviceListerFunc != nil {
		return m.NewDeviceListerFunc(ffmpegPath)
	}
	if m.mockDeviceLister != nil {
		return m.mockDeviceLister, nil
	}
	return &mockDeviceLister{}, nil
}

type mockDeviceLister struct {
	ListDevicesFunc func(ctx context.Context) ([]string, error)
}

func (m *mockDeviceLister) ListDevices(ctx context.Context) ([]string, error) {
	if m.ListDevicesFunc != nil {
		return m.ListDevicesFunc(ctx)
	}
	return nil, nil
}

// ---------------------------------------------------------------------------
// Compile-time interface verification
// ---------------------------------------------------------------------------

var (
	_ FFmpegResolver         = (*mockFFmpegResolver)(nil)
	_ ConfigLoader           = (*mockConfigLoader)(nil)
	_ TranscriberFactory     = (*mockTranscriberFactory)(nil)
	_ transcribe.Transcriber = (*mockTranscriber)(nil)
	_ RestructurerFactory    = (*mockRestructurerFactory)(nil)
	_ restructure.MapReducer = (*mockMapReduceRestructurer)(nil)
	_ ChunkerFactory         = (*mockChunkerFactory)(nil)
	_ audio.Chunker          = (*mockChunker)(nil)
	_ RecorderFactory        = (*mockRecorderFactory)(nil)
	_ audio.Recorder         = (*mockRecorder)(nil)
	_ DeviceListerFactory    = (*mockDeviceListerFactory)(nil)
	_ audio.DeviceLister     = (*mockDeviceLister)(nil)
)
