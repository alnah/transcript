package transcribe_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alnah/transcript/internal/apierr"
	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/transcribe"
)

// Notes:
// - Black-box testing via package transcribe_test.
// - Uses export_test.go NewTestTranscriber(httpClient, baseURL, opts...) for injection.
// - All API mocking uses httptest.Server or mockHTTPClient.
// - Tests use short delays (1ms) to avoid slow tests while still exercising backoff.
// - Parallelism tests use channel-based synchronization, not timing.
//
// Coverage gaps (intentional):
// - Exact backoff timing (1s, 2s, 4s...) -- implementation detail.
// - Precise maxParallel verification -- only smoke-tested via channel blocking.
// - Network I/O with real OpenAI client -- requires integration tests.
// - RetryWithBackoff tests moved to apierr/retry_test.go.

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

// mockHTTPClient implements httpDoer for testing HTTP calls.
type mockHTTPClient struct {
	mu              sync.Mutex
	requests        []*http.Request
	requestBodies   [][]byte
	responses       []*http.Response
	errors          []error
	callIndex       int
	statusCode      int
	responseBody    string
	chunkingCapture string
}

func newMockHTTPClient(statusCode int, responseBody string) *mockHTTPClient {
	return &mockHTTPClient{
		statusCode:   statusCode,
		responseBody: responseBody,
	}
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Capture request body for verification
	if req.Body != nil {
		body, _ := io.ReadAll(req.Body)
		m.requestBodies = append(m.requestBodies, body)
		if bytes.Contains(body, []byte("chunking_strategy")) {
			m.chunkingCapture = "found"
			if idx := bytes.Index(body, []byte("chunking_strategy")); idx != -1 {
				if bytes.Contains(body[idx:], []byte("auto")) {
					m.chunkingCapture = "auto"
				}
			}
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}

	m.requests = append(m.requests, req)

	idx := m.callIndex
	m.callIndex++

	if idx < len(m.errors) && m.errors[idx] != nil {
		return nil, m.errors[idx]
	}

	if idx < len(m.responses) {
		return m.responses[idx], nil
	}

	return &http.Response{
		StatusCode: m.statusCode,
		Body:       io.NopCloser(bytes.NewReader([]byte(m.responseBody))),
		Header:     make(http.Header),
	}, nil
}

func (m *mockHTTPClient) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.requests)
}

func (m *mockHTTPClient) HasChunkingStrategy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chunkingCapture != ""
}

func (m *mockHTTPClient) ChunkingStrategyValue() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.chunkingCapture
}

// createTempAudioFile creates a temporary file for testing.
func createTempAudioFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.ogg")
	if err := os.WriteFile(path, []byte("fake audio content"), 0644); err != nil {
		t.Fatalf("failed to create temp audio file: %v", err)
	}
	return path
}

// mockOpenAIServer creates a test server that returns predefined transcription responses.
type mockOpenAIServer struct {
	*httptest.Server
	mu          sync.Mutex
	calls       []openAITranscribeCall
	responses   []mockResponse
	responseIdx int
}

type openAITranscribeCall struct {
	Model    string
	Language string
	Prompt   string
	Format   string
	HasFile  bool
}

type mockResponse struct {
	statusCode int
	body       any
}

func newMockOpenAIServer() *mockOpenAIServer {
	m := &mockOpenAIServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()

		// Parse multipart form
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		call := openAITranscribeCall{
			Model:    r.FormValue("model"),
			Language: r.FormValue("language"),
			Prompt:   r.FormValue("prompt"),
			Format:   r.FormValue("response_format"),
		}
		if _, _, err := r.FormFile("file"); err == nil {
			call.HasFile = true
		}
		m.calls = append(m.calls, call)

		// Get response
		var resp mockResponse
		if m.responseIdx < len(m.responses) {
			resp = m.responses[m.responseIdx]
			m.responseIdx++
		} else if len(m.responses) > 0 {
			resp = m.responses[len(m.responses)-1]
		} else {
			resp = mockResponse{
				statusCode: http.StatusOK,
				body:       map[string]any{"text": "default response"},
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.statusCode)
		json.NewEncoder(w).Encode(resp.body)
	}))
	return m
}

func (m *mockOpenAIServer) addResponse(statusCode int, body any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, mockResponse{statusCode, body})
}

func (m *mockOpenAIServer) lastCall() openAITranscribeCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return openAITranscribeCall{}
	}
	return m.calls[len(m.calls)-1]
}

// mockTranscriber implements transcribe.Transcriber for TranscribeAll tests.
type mockTranscriber struct {
	mu         sync.Mutex
	results    map[string]string
	errors     map[string]error
	blocking   chan struct{}
	started    chan struct{}
	concurrent int32
	maxConc    int32
}

func newMockTranscriber() *mockTranscriber {
	return &mockTranscriber{
		results: make(map[string]string),
		errors:  make(map[string]error),
	}
}

func (m *mockTranscriber) Transcribe(ctx context.Context, audioPath string, opts transcribe.Options) (string, error) {
	current := atomic.AddInt32(&m.concurrent, 1)
	defer atomic.AddInt32(&m.concurrent, -1)

	for {
		old := atomic.LoadInt32(&m.maxConc)
		if current <= old || atomic.CompareAndSwapInt32(&m.maxConc, old, current) {
			break
		}
	}

	if m.started != nil {
		select {
		case m.started <- struct{}{}:
		default:
		}
	}

	if m.blocking != nil {
		select {
		case <-m.blocking:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}

	m.mu.Lock()
	err := m.errors[audioPath]
	result := m.results[audioPath]
	m.mu.Unlock()

	if err != nil {
		return "", err
	}
	return result, nil
}

// ---------------------------------------------------------------------------
// TestNewOpenAITranscriber - Constructor and options
// ---------------------------------------------------------------------------

func TestNewOpenAITranscriber(t *testing.T) {
	t.Parallel()

	t.Run("creates transcriber with defaults", func(t *testing.T) {
		t.Parallel()

		audioPath := createTempAudioFile(t)
		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, map[string]any{"text": "hello"})

		tr := transcribe.NewTestTranscriber(server.Client(), server.URL)

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}
		if result != "hello" {
			t.Errorf("got %q, want %q", result, "hello")
		}
	})
}

// ---------------------------------------------------------------------------
// TestTranscribe_Success - Successful transcription cases
// ---------------------------------------------------------------------------

func TestTranscribe_Success(t *testing.T) {
	t.Parallel()

	t.Run("returns text from response", func(t *testing.T) {
		t.Parallel()

		audioPath := createTempAudioFile(t)
		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, map[string]any{"text": "transcribed text"})

		tr := transcribe.NewTestTranscriber(server.Client(), server.URL)

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}
		if result != "transcribed text" {
			t.Errorf("got %q, want %q", result, "transcribed text")
		}
	})

	t.Run("passes language as base code", func(t *testing.T) {
		t.Parallel()

		audioPath := createTempAudioFile(t)
		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, map[string]any{"text": "bonjour"})

		tr := transcribe.NewTestTranscriber(server.Client(), server.URL)

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Language: lang.MustParse("fr-FR"),
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}

		call := server.lastCall()
		if call.Language != "fr" {
			t.Errorf("Language = %q, want %q", call.Language, "fr")
		}
	})

	t.Run("passes prompt to API", func(t *testing.T) {
		t.Parallel()

		audioPath := createTempAudioFile(t)
		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, map[string]any{"text": "kubernetes discussion"})

		tr := transcribe.NewTestTranscriber(server.Client(), server.URL)

		prompt := "Technical discussion about Kubernetes"
		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Prompt: prompt,
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}

		call := server.lastCall()
		if call.Prompt != prompt {
			t.Errorf("Prompt = %q, want %q", call.Prompt, prompt)
		}
	})

	t.Run("uses correct model for standard transcription", func(t *testing.T) {
		t.Parallel()

		audioPath := createTempAudioFile(t)
		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, map[string]any{"text": "text"})

		tr := transcribe.NewTestTranscriber(server.Client(), server.URL)

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: false,
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}

		call := server.lastCall()
		if call.Model != transcribe.ModelGPT4oMiniTranscribe {
			t.Errorf("Model = %q, want %q", call.Model, transcribe.ModelGPT4oMiniTranscribe)
		}
	})

	t.Run("uses diarize model when diarize is true", func(t *testing.T) {
		t.Parallel()

		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusOK, `{"text": "diarized text", "segments": []}`)

		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(0),
		)

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: true,
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}
		if result != "diarized text" {
			t.Errorf("got %q, want %q", result, "diarized text")
		}

		if httpMock.CallCount() != 1 {
			t.Errorf("HTTP call count = %d, want 1", httpMock.CallCount())
		}
	})
}

// ---------------------------------------------------------------------------
// TestTranscribe_Diarization - Diarized output formatting via HTTP
// ---------------------------------------------------------------------------

func TestTranscribe_Diarization(t *testing.T) {
	t.Parallel()

	t.Run("formats segments with speaker labels", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		responseJSON := `{
			"text": "Hello there General Kenobi",
			"segments": [
				{"id": "seg_001", "start": 0.0, "end": 1.5, "text": "Hello there", "speaker": "Speaker A"},
				{"id": "seg_002", "start": 1.5, "end": 3.0, "text": "General Kenobi", "speaker": "Speaker B"}
			]
		}`
		httpMock := newMockHTTPClient(http.StatusOK, responseJSON)

		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test")

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: true,
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}

		speakerPattern := regexp.MustCompile(`\[Speaker [AB]\]`)
		matches := speakerPattern.FindAllString(result, -1)
		if len(matches) != 2 {
			t.Errorf("expected 2 speaker markers, got %d in: %q", len(matches), result)
		}

		if !regexp.MustCompile(`Hello there`).MatchString(result) {
			t.Errorf("result should contain 'Hello there': %q", result)
		}
		if !regexp.MustCompile(`General Kenobi`).MatchString(result) {
			t.Errorf("result should contain 'General Kenobi': %q", result)
		}
	})

	t.Run("falls back to text when no segments", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		responseJSON := `{"text": "fallback text", "segments": []}`
		httpMock := newMockHTTPClient(http.StatusOK, responseJSON)

		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test")

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: true,
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}

		if result != "fallback text" {
			t.Errorf("got %q, want %q", result, "fallback text")
		}
	})

	t.Run("uses speaker ID as fallback when speaker field empty", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		responseJSON := `{
			"text": "Hello",
			"segments": [
				{"id": "seg_001", "start": 0.0, "end": 1.0, "text": "Hello", "speaker": ""}
			]
		}`
		httpMock := newMockHTTPClient(http.StatusOK, responseJSON)

		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test")

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: true,
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}

		if !regexp.MustCompile(`\[Speaker seg_001\]`).MatchString(result) {
			t.Errorf("expected speaker ID fallback in: %q", result)
		}
	})

	t.Run("sends chunking_strategy auto parameter", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		responseJSON := `{"text": "transcribed", "segments": []}`
		httpMock := newMockHTTPClient(http.StatusOK, responseJSON)

		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test")

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: true,
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}

		if !httpMock.HasChunkingStrategy() {
			t.Error("chunking_strategy was not included in request")
		}
		if httpMock.ChunkingStrategyValue() != "auto" {
			t.Errorf("chunking_strategy = %q, want %q", httpMock.ChunkingStrategyValue(), "auto")
		}
	})

	t.Run("returns error for nonexistent audio file", func(t *testing.T) {
		t.Parallel()

		httpMock := newMockHTTPClient(http.StatusOK, `{"text": "ok", "segments": []}`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test")

		_, err := tr.Transcribe(context.Background(), "/nonexistent/path/audio.ogg", transcribe.Options{
			Diarize: true,
		})
		if err == nil {
			t.Fatal("expected error for nonexistent file, got nil")
		}
		if httpMock.CallCount() != 0 {
			t.Errorf("HTTP call count = %d, want 0", httpMock.CallCount())
		}
	})

	t.Run("passes language to diarization request", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusOK, `{"text": "bonjour", "segments": []}`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test")

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize:  true,
			Language: lang.MustParse("fr-FR"),
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}

		if len(httpMock.requestBodies) == 0 {
			t.Fatal("no request body captured")
		}
		body := string(httpMock.requestBodies[0])
		if !strings.Contains(body, "language") {
			t.Error("language field not found in request")
		}
	})
}

// ---------------------------------------------------------------------------
// TestTranscribe_DiarizationErrors - HTTP error handling for diarization
// ---------------------------------------------------------------------------

func TestTranscribe_DiarizationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		statusCode   int
		responseBody string
		wantSentinel error
	}{
		{
			name:         "401 unauthorized returns ErrAuthFailed",
			statusCode:   http.StatusUnauthorized,
			responseBody: `{"error": {"message": "Invalid API key", "type": "invalid_request_error"}}`,
			wantSentinel: apierr.ErrAuthFailed,
		},
		{
			name:         "429 rate limit returns ErrRateLimit",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"error": {"message": "Rate limit exceeded", "type": "rate_limit_error"}}`,
			wantSentinel: apierr.ErrRateLimit,
		},
		{
			name:         "429 with quota message returns ErrQuotaExceeded",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"error": {"message": "You exceeded your quota", "type": "insufficient_quota"}}`,
			wantSentinel: apierr.ErrQuotaExceeded,
		},
		{
			name:         "429 with billing message returns ErrQuotaExceeded",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"error": {"message": "Please check your billing details", "type": "billing_error"}}`,
			wantSentinel: apierr.ErrQuotaExceeded,
		},
		{
			name:         "400 bad request returns ErrBadRequest",
			statusCode:   http.StatusBadRequest,
			responseBody: `{"error": {"message": "Invalid file format", "type": "invalid_request_error"}}`,
			wantSentinel: apierr.ErrBadRequest,
		},
		{
			name:         "408 timeout returns ErrTimeout",
			statusCode:   http.StatusRequestTimeout,
			responseBody: `{"error": {"message": "Request timeout", "type": "timeout"}}`,
			wantSentinel: apierr.ErrTimeout,
		},
		{
			name:         "504 gateway timeout returns ErrTimeout",
			statusCode:   http.StatusGatewayTimeout,
			responseBody: `{"error": {"message": "Gateway timeout", "type": "timeout"}}`,
			wantSentinel: apierr.ErrTimeout,
		},
		{
			name:         "500 server error returns ErrTimeout (retryable)",
			statusCode:   http.StatusInternalServerError,
			responseBody: `{"error": {"message": "Internal server error", "type": "server_error"}}`,
			wantSentinel: apierr.ErrTimeout,
		},
		{
			name:         "502 bad gateway returns ErrTimeout (retryable)",
			statusCode:   http.StatusBadGateway,
			responseBody: `{"error": {"message": "Bad gateway", "type": "server_error"}}`,
			wantSentinel: apierr.ErrTimeout,
		},
		{
			name:         "503 service unavailable returns ErrTimeout (retryable)",
			statusCode:   http.StatusServiceUnavailable,
			responseBody: `{"error": {"message": "Service unavailable", "type": "server_error"}}`,
			wantSentinel: apierr.ErrTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			audioPath := createTempAudioFile(t)

			httpMock := newMockHTTPClient(tt.statusCode, tt.responseBody)
			tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
				transcribe.WithMaxRetries(0),
			)

			_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
				Diarize: true,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tt.wantSentinel) {
				t.Errorf("error = %v, want sentinel %v", err, tt.wantSentinel)
			}
		})
	}

	t.Run("malformed JSON error response returns generic error", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusBadRequest, `not valid json`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(0),
		)

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: true,
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, apierr.ErrBadRequest) {
			t.Errorf("error = %v, want ErrBadRequest", err)
		}
	})

	t.Run("diarization retries on rate limit", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := &mockHTTPClient{
			statusCode:   http.StatusOK,
			responseBody: `{"text": "success", "segments": []}`,
			responses: []*http.Response{
				{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"error": {"message": "Rate limit"}}`))),
					Header:     make(http.Header),
				},
				{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"text": "success after retry", "segments": []}`))),
					Header:     make(http.Header),
				},
			},
		}

		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(3),
			transcribe.WithRetryDelays(1*time.Millisecond, 10*time.Millisecond),
		)

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: true,
		})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}
		if result != "success after retry" {
			t.Errorf("got %q, want %q", result, "success after retry")
		}
		if httpMock.CallCount() != 2 {
			t.Errorf("HTTP call count = %d, want 2", httpMock.CallCount())
		}
	})

	t.Run("diarization does not retry on auth failure", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusUnauthorized, `{"error": {"message": "Invalid API key"}}`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(5),
			transcribe.WithRetryDelays(1*time.Millisecond, 10*time.Millisecond),
		)

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: true,
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if httpMock.CallCount() != 1 {
			t.Errorf("HTTP call count = %d, want 1 (no retry)", httpMock.CallCount())
		}
	})

	t.Run("malformed diarize response returns parse error", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusOK, `{"text": "ok", "segments": "not_an_array"}`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(0),
		)

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{
			Diarize: true,
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !regexp.MustCompile(`failed to parse`).MatchString(err.Error()) {
			t.Errorf("error should mention parse failure: %v", err)
		}
	})
}

// ---------------------------------------------------------------------------
// TestTranscribe_ErrorClassification - Error wrapping and sentinel errors via HTTP
// ---------------------------------------------------------------------------

func TestTranscribe_ErrorClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		statusCode   int
		responseBody string
		wantSentinel error
	}{
		{
			name:         "401 unauthorized returns ErrAuthFailed",
			statusCode:   http.StatusUnauthorized,
			responseBody: `{"error": {"message": "Invalid API key"}}`,
			wantSentinel: apierr.ErrAuthFailed,
		},
		{
			name:         "429 with quota message returns ErrQuotaExceeded",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"error": {"message": "You have exceeded your quota"}}`,
			wantSentinel: apierr.ErrQuotaExceeded,
		},
		{
			name:         "429 with billing message returns ErrQuotaExceeded",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"error": {"message": "Please check your billing details"}}`,
			wantSentinel: apierr.ErrQuotaExceeded,
		},
		{
			name:         "429 rate limit returns ErrRateLimit",
			statusCode:   http.StatusTooManyRequests,
			responseBody: `{"error": {"message": "Rate limit exceeded"}}`,
			wantSentinel: apierr.ErrRateLimit,
		},
		{
			name:         "408 timeout returns ErrTimeout",
			statusCode:   http.StatusRequestTimeout,
			responseBody: `{"error": {"message": "Request timeout"}}`,
			wantSentinel: apierr.ErrTimeout,
		},
		{
			name:         "504 gateway timeout returns ErrTimeout",
			statusCode:   http.StatusGatewayTimeout,
			responseBody: `{"error": {"message": "Gateway timeout"}}`,
			wantSentinel: apierr.ErrTimeout,
		},
		{
			name:         "400 bad request returns ErrBadRequest",
			statusCode:   http.StatusBadRequest,
			responseBody: `{"error": {"message": "Invalid request"}}`,
			wantSentinel: apierr.ErrBadRequest,
		},
		{
			name:         "403 forbidden returns ErrBadRequest",
			statusCode:   http.StatusForbidden,
			responseBody: `{"error": {"message": "Access denied"}}`,
			wantSentinel: apierr.ErrBadRequest,
		},
		{
			name:         "404 not found returns ErrBadRequest",
			statusCode:   http.StatusNotFound,
			responseBody: `{"error": {"message": "Model not found"}}`,
			wantSentinel: apierr.ErrBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			audioPath := createTempAudioFile(t)

			httpMock := newMockHTTPClient(tt.statusCode, tt.responseBody)
			tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
				transcribe.WithMaxRetries(0),
			)

			_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if !errors.Is(err, tt.wantSentinel) {
				t.Errorf("error = %v, want sentinel %v", err, tt.wantSentinel)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTranscribe_Retry - Retry behavior with backoff
// ---------------------------------------------------------------------------

func TestTranscribe_Retry(t *testing.T) {
	t.Parallel()

	t.Run("retries on rate limit and succeeds", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := &mockHTTPClient{
			responses: []*http.Response{
				{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"error": {"message": "Rate limit exceeded"}}`))),
					Header:     make(http.Header),
				},
				{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"error": {"message": "Rate limit exceeded"}}`))),
					Header:     make(http.Header),
				},
				{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"text": "success"}`))),
					Header:     make(http.Header),
				},
			},
		}

		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(5),
			transcribe.WithRetryDelays(1*time.Millisecond, 10*time.Millisecond),
		)

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}
		if result != "success" {
			t.Errorf("got %q, want %q", result, "success")
		}
		if httpMock.CallCount() != 3 {
			t.Errorf("call count = %d, want 3", httpMock.CallCount())
		}
	})

	t.Run("retries on server error 500", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := &mockHTTPClient{
			responses: []*http.Response{
				{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"error": {"message": "Internal server error"}}`))),
					Header:     make(http.Header),
				},
				{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"text": "recovered"}`))),
					Header:     make(http.Header),
				},
			},
		}

		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(3),
			transcribe.WithRetryDelays(1*time.Millisecond, 10*time.Millisecond),
		)

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}
		if result != "recovered" {
			t.Errorf("got %q, want %q", result, "recovered")
		}
	})

	t.Run("does not retry on auth failure", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusUnauthorized, `{"error": {"message": "Invalid API key"}}`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(5),
			transcribe.WithRetryDelays(1*time.Millisecond, 10*time.Millisecond),
		)

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if httpMock.CallCount() != 1 {
			t.Errorf("call count = %d, want 1 (no retry)", httpMock.CallCount())
		}
	})

	t.Run("does not retry on quota exceeded", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusTooManyRequests, `{"error": {"message": "You exceeded your quota"}}`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(5),
			transcribe.WithRetryDelays(1*time.Millisecond, 10*time.Millisecond),
		)

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if httpMock.CallCount() != 1 {
			t.Errorf("call count = %d, want 1 (no retry)", httpMock.CallCount())
		}
	})

	t.Run("max retries exceeded wraps error", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusTooManyRequests, `{"error": {"message": "Rate limit exceeded"}}`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(2),
			transcribe.WithRetryDelays(1*time.Millisecond, 10*time.Millisecond),
		)

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if httpMock.CallCount() != 3 {
			t.Errorf("call count = %d, want 3", httpMock.CallCount())
		}

		if !regexp.MustCompile(`max retries.*exceeded`).MatchString(err.Error()) {
			t.Errorf("error should mention max retries: %v", err)
		}
	})

	t.Run("context cancellation stops retries", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusTooManyRequests, `{"error": {"message": "Rate limit exceeded"}}`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(10),
			transcribe.WithRetryDelays(50*time.Millisecond, 100*time.Millisecond),
		)

		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			time.Sleep(10 * time.Millisecond)
			cancel()
		}()

		_, err := tr.Transcribe(ctx, audioPath, transcribe.Options{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
		if httpMock.CallCount() >= 5 {
			t.Errorf("call count = %d, should be less than 5 (cancelled early)", httpMock.CallCount())
		}
	})
}

// ---------------------------------------------------------------------------
// TestTranscribe_Options - Option functions
// ---------------------------------------------------------------------------

func TestTranscribe_Options(t *testing.T) {
	t.Parallel()

	t.Run("WithMaxRetries(0) disables retries", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := newMockHTTPClient(http.StatusTooManyRequests, `{"error": {"message": "Rate limit exceeded"}}`)
		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(0),
		)

		_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if httpMock.CallCount() != 1 {
			t.Errorf("call count = %d, want 1 (no retries)", httpMock.CallCount())
		}
	})

	t.Run("WithMaxRetries negative is ignored", func(t *testing.T) {
		t.Parallel()
		audioPath := createTempAudioFile(t)

		httpMock := &mockHTTPClient{
			responses: []*http.Response{
				{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"error": {"message": "Rate limit exceeded"}}`))),
					Header:     make(http.Header),
				},
				{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader([]byte(`{"text": "ok"}`))),
					Header:     make(http.Header),
				},
			},
		}

		tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
			transcribe.WithMaxRetries(-1),
			transcribe.WithRetryDelays(1*time.Millisecond, 10*time.Millisecond),
		)

		result, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
		if err != nil {
			t.Errorf("Transcribe() unexpected error: %v", err)
		}
		if result != "ok" {
			t.Errorf("got %q, want %q", result, "ok")
		}
		if httpMock.CallCount() != 2 {
			t.Errorf("call count = %d, want 2", httpMock.CallCount())
		}
	})
}

// ---------------------------------------------------------------------------
// TestClassifyError - Exported internal function
// ---------------------------------------------------------------------------

func TestClassifyError(t *testing.T) {
	t.Parallel()

	t.Run("non-API error passes through", func(t *testing.T) {
		t.Parallel()

		originalErr := errors.New("network error")
		result := transcribe.ClassifyError(originalErr)

		if result != originalErr {
			t.Errorf("error should pass through unchanged: got %v, want %v", result, originalErr)
		}
	})

	t.Run("nil error returns nil", func(t *testing.T) {
		t.Parallel()

		result := transcribe.ClassifyError(nil)
		if result != nil {
			t.Errorf("ClassifyError(nil) = %v, want nil", result)
		}
	})
}

// ---------------------------------------------------------------------------
// TestClassifyError_ViaHTTP - Error classification tested end-to-end through HTTP
// ---------------------------------------------------------------------------

func TestClassifyError_ViaHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		statusCode   int
		body         string
		wantSentinel error
	}{
		{
			name:         "400 Bad Request returns ErrBadRequest",
			statusCode:   http.StatusBadRequest,
			body:         `{"error": {"message": "Invalid request"}}`,
			wantSentinel: apierr.ErrBadRequest,
		},
		{
			name:         "403 Forbidden returns ErrBadRequest",
			statusCode:   http.StatusForbidden,
			body:         `{"error": {"message": "Access denied"}}`,
			wantSentinel: apierr.ErrBadRequest,
		},
		{
			name:         "404 Not Found returns ErrBadRequest",
			statusCode:   http.StatusNotFound,
			body:         `{"error": {"message": "Model not found"}}`,
			wantSentinel: apierr.ErrBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			audioPath := createTempAudioFile(t)

			httpMock := newMockHTTPClient(tt.statusCode, tt.body)
			tr := transcribe.NewTestTranscriber(httpMock, "http://fake-api.test",
				transcribe.WithMaxRetries(0),
			)

			_, err := tr.Transcribe(context.Background(), audioPath, transcribe.Options{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tt.wantSentinel) {
				t.Errorf("expected %v, got %v", tt.wantSentinel, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestIsRetryableError - Exported internal function
// ---------------------------------------------------------------------------

func TestIsRetryableError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "ErrRateLimit is retryable",
			err:  apierr.ErrRateLimit,
			want: true,
		},
		{
			name: "ErrTimeout is retryable",
			err:  apierr.ErrTimeout,
			want: true,
		},
		{
			name: "wrapped ErrRateLimit is retryable",
			err:  errors.Join(errors.New("context"), apierr.ErrRateLimit),
			want: true,
		},
		{
			name: "ErrAuthFailed is not retryable",
			err:  apierr.ErrAuthFailed,
			want: false,
		},
		{
			name: "ErrQuotaExceeded is not retryable",
			err:  apierr.ErrQuotaExceeded,
			want: false,
		},
		{
			name: "context.Canceled is not retryable",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "random error is not retryable",
			err:  errors.New("random error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := transcribe.IsRetryableError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestTranscribeAll - Parallel batch transcription
// ---------------------------------------------------------------------------

func TestTranscribeAll(t *testing.T) {
	t.Parallel()

	t.Run("empty chunks returns nil", func(t *testing.T) {
		t.Parallel()

		results, err := transcribe.TranscribeAll(
			context.Background(),
			nil,
			newMockTranscriber(),
			transcribe.Options{},
			4,
		)

		if err != nil {
			t.Errorf("TranscribeAll() unexpected error: %v", err)
		}
		if results != nil {
			t.Errorf("got %v, want nil", results)
		}
	})

	t.Run("single chunk returns single result", func(t *testing.T) {
		t.Parallel()

		mock := newMockTranscriber()
		mock.results["/path/chunk0.mp3"] = "hello world"

		chunks := []audio.Chunk{
			{Path: "/path/chunk0.mp3", Index: 0},
		}

		results, err := transcribe.TranscribeAll(
			context.Background(),
			chunks,
			mock,
			transcribe.Options{},
			4,
		)

		if err != nil {
			t.Errorf("TranscribeAll() unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
		if results[0] != "hello world" {
			t.Errorf("results[0] = %q, want %q", results[0], "hello world")
		}
	})

	t.Run("multiple chunks return results in order", func(t *testing.T) {
		t.Parallel()

		mock := newMockTranscriber()
		mock.results["/path/chunk0.mp3"] = "first"
		mock.results["/path/chunk1.mp3"] = "second"
		mock.results["/path/chunk2.mp3"] = "third"

		chunks := []audio.Chunk{
			{Path: "/path/chunk0.mp3", Index: 0},
			{Path: "/path/chunk1.mp3", Index: 1},
			{Path: "/path/chunk2.mp3", Index: 2},
		}

		results, err := transcribe.TranscribeAll(
			context.Background(),
			chunks,
			mock,
			transcribe.Options{},
			4,
		)

		if err != nil {
			t.Errorf("TranscribeAll() unexpected error: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("got %d results, want 3", len(results))
		}
		if results[0] != "first" || results[1] != "second" || results[2] != "third" {
			t.Errorf("results = %v, want [first, second, third]", results)
		}
	})

	t.Run("first error aborts and returns error", func(t *testing.T) {
		t.Parallel()

		mock := newMockTranscriber()
		mock.results["/path/chunk0.mp3"] = "ok"
		mock.errors["/path/chunk1.mp3"] = errors.New("transcription failed")
		mock.results["/path/chunk2.mp3"] = "ok"

		chunks := []audio.Chunk{
			{Path: "/path/chunk0.mp3", Index: 0},
			{Path: "/path/chunk1.mp3", Index: 1},
			{Path: "/path/chunk2.mp3", Index: 2},
		}

		_, err := transcribe.TranscribeAll(
			context.Background(),
			chunks,
			mock,
			transcribe.Options{},
			4,
		)

		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !regexp.MustCompile(`chunk 1`).MatchString(err.Error()) {
			t.Errorf("error should mention chunk index: %v", err)
		}
	})

	t.Run("context cancellation propagates", func(t *testing.T) {
		t.Parallel()

		mock := newMockTranscriber()
		mock.blocking = make(chan struct{})
		mock.started = make(chan struct{}, 10)

		chunks := []audio.Chunk{
			{Path: "/path/chunk0.mp3", Index: 0},
			{Path: "/path/chunk1.mp3", Index: 1},
		}

		ctx, cancel := context.WithCancel(context.Background())

		done := make(chan error)
		go func() {
			_, err := transcribe.TranscribeAll(ctx, chunks, mock, transcribe.Options{}, 4)
			done <- err
		}()

		<-mock.started
		cancel()

		err := <-done
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	})

	t.Run("maxParallel 1 serializes requests", func(t *testing.T) {
		t.Parallel()

		mock := newMockTranscriber()
		mock.results["/path/chunk0.mp3"] = "a"
		mock.results["/path/chunk1.mp3"] = "b"
		mock.results["/path/chunk2.mp3"] = "c"

		chunks := []audio.Chunk{
			{Path: "/path/chunk0.mp3", Index: 0},
			{Path: "/path/chunk1.mp3", Index: 1},
			{Path: "/path/chunk2.mp3", Index: 2},
		}

		results, err := transcribe.TranscribeAll(
			context.Background(),
			chunks,
			mock,
			transcribe.Options{},
			1,
		)

		if err != nil {
			t.Errorf("TranscribeAll() unexpected error: %v", err)
		}
		if len(results) != 3 {
			t.Fatalf("got %d results, want 3", len(results))
		}

		if atomic.LoadInt32(&mock.maxConc) > 1 {
			t.Errorf("maxConcurrent = %d, want <= 1", mock.maxConc)
		}
	})

	t.Run("maxParallel 0 treated as 1", func(t *testing.T) {
		t.Parallel()

		mock := newMockTranscriber()
		mock.results["/path/chunk0.mp3"] = "ok"

		chunks := []audio.Chunk{
			{Path: "/path/chunk0.mp3", Index: 0},
		}

		results, err := transcribe.TranscribeAll(
			context.Background(),
			chunks,
			mock,
			transcribe.Options{},
			0,
		)

		if err != nil {
			t.Errorf("TranscribeAll() unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
	})

	t.Run("negative maxParallel treated as 1", func(t *testing.T) {
		t.Parallel()

		mock := newMockTranscriber()
		mock.results["/path/chunk0.mp3"] = "ok"

		chunks := []audio.Chunk{
			{Path: "/path/chunk0.mp3", Index: 0},
		}

		results, err := transcribe.TranscribeAll(
			context.Background(),
			chunks,
			mock,
			transcribe.Options{},
			-5,
		)

		if err != nil {
			t.Errorf("TranscribeAll() unexpected error: %v", err)
		}
		if len(results) != 1 {
			t.Fatalf("got %d results, want 1", len(results))
		}
	})
}
