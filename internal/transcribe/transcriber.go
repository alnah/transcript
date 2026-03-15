package transcribe

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/alnah/transcript/internal/apierr"
	"github.com/alnah/transcript/internal/audio"
	"github.com/alnah/transcript/internal/lang"
)

// OpenAI transcription model and format identifiers.
const (
	// ModelGPT4oMiniTranscribe is the cost-effective transcription model ($0.003/min).
	ModelGPT4oMiniTranscribe = "gpt-4o-mini-transcribe"

	// ModelGPT4oTranscribeDiarize is the transcription model with speaker identification.
	ModelGPT4oTranscribeDiarize = "gpt-4o-transcribe-diarize"

	// FormatDiarizedJSON is the response format for diarized transcription.
	FormatDiarizedJSON = "diarized_json"

	// ChunkingStrategyAuto lets OpenAI automatically determine chunking boundaries.
	// Required for diarization model when input is longer than 30 seconds.
	ChunkingStrategyAuto = "auto"

	// defaultOpenAIBaseURL is the default base URL for the OpenAI API.
	defaultOpenAIBaseURL = "https://api.openai.com"

	// transcriptionPath is the API path for audio transcription.
	transcriptionPath = "/v1/audio/transcriptions"
)

// Parallelism configuration.
const (
	// MaxRecommendedParallel is the recommended upper limit for concurrent API requests.
	// Higher values may trigger rate limiting.
	MaxRecommendedParallel = 10
)

// Default retry configuration per specification.
const (
	defaultMaxRetries = 5
	defaultBaseDelay  = 1 * time.Second
	defaultMaxDelay   = 30 * time.Second
)

// Response size limit to prevent OOM from malformed responses (10MB).
const maxResponseSize = 10 * 1024 * 1024

// Options configures transcription behavior.
type Options struct {
	// Diarize enables speaker identification in the transcript.
	// When true, uses gpt-4o-transcribe-diarize model.
	Diarize bool

	// Prompt provides context to improve transcription accuracy.
	// Useful for domain-specific vocabulary, acronyms, or expected content.
	// Example: "Technical discussion about Kubernetes and Docker containers."
	// Note: Prompt can also hint at the language if Language is not set.
	Prompt string

	// Language specifies the audio language.
	// Zero value means auto-detect (recommended for most use cases).
	Language lang.Language
}

// Transcriber transcribes audio files to text.
type Transcriber interface {
	// Transcribe converts an audio file to text.
	// audioPath must be a file in a supported format: mp3, mp4, mpeg, mpga, m4a, wav, webm, ogg.
	// Returns the transcribed text or an error.
	Transcribe(ctx context.Context, audioPath string, opts Options) (string, error)
}

// httpDoer abstracts HTTP client for testing.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Compile-time interface compliance check.
var _ Transcriber = (*OpenAITranscriber)(nil)

// OpenAITranscriber transcribes audio using OpenAI's REST API.
// It supports standard transcription and speaker diarization.
// Automatic retries with exponential backoff for transient errors.
type OpenAITranscriber struct {
	httpClient httpDoer
	apiKey     string
	baseURL    string
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
}

// TranscriberOption configures an OpenAITranscriber.
type TranscriberOption func(*OpenAITranscriber)

// WithMaxRetries sets the maximum number of retry attempts.
func WithMaxRetries(n int) TranscriberOption {
	return func(t *OpenAITranscriber) {
		if n >= 0 {
			t.maxRetries = n
		}
	}
}

// WithRetryDelays sets the base and max delays for exponential backoff.
func WithRetryDelays(base, max time.Duration) TranscriberOption {
	return func(t *OpenAITranscriber) {
		if base > 0 {
			t.baseDelay = base
		}
		if max > 0 {
			t.maxDelay = max
		}
	}
}

// WithHTTPClient sets a custom HTTP client (for testing).
func WithHTTPClient(c httpDoer) TranscriberOption {
	return func(t *OpenAITranscriber) {
		t.httpClient = c
	}
}

// WithBaseURL sets a custom base URL (for testing or proxies).
func WithBaseURL(url string) TranscriberOption {
	return func(t *OpenAITranscriber) {
		t.baseURL = strings.TrimSuffix(url, "/")
	}
}

// NewOpenAITranscriber creates a new OpenAITranscriber.
// apiKey is required for all requests (used as Bearer token).
func NewOpenAITranscriber(apiKey string, opts ...TranscriberOption) *OpenAITranscriber {
	t := &OpenAITranscriber{
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		apiKey:     apiKey,
		baseURL:    defaultOpenAIBaseURL,
		maxRetries: defaultMaxRetries,
		baseDelay:  defaultBaseDelay,
		maxDelay:   defaultMaxDelay,
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// Transcribe transcribes an audio file using OpenAI's API.
// It automatically retries on transient errors (rate limits, timeouts, server errors).
func (t *OpenAITranscriber) Transcribe(ctx context.Context, audioPath string, opts Options) (string, error) {
	if opts.Diarize {
		return t.transcribeWithRetry(ctx, audioPath, opts, ModelGPT4oTranscribeDiarize, FormatDiarizedJSON, true)
	}
	return t.transcribeWithRetry(ctx, audioPath, opts, ModelGPT4oMiniTranscribe, "json", false)
}

// transcribeWithRetry executes the transcription with exponential backoff retry.
func (t *OpenAITranscriber) transcribeWithRetry(ctx context.Context, audioPath string, opts Options, model, format string, diarize bool) (string, error) {
	cfg := apierr.RetryConfig{
		MaxRetries: t.maxRetries,
		BaseDelay:  t.baseDelay,
		MaxDelay:   t.maxDelay,
	}

	return apierr.RetryWithBackoff(ctx, cfg, func() (string, error) {
		result, err := t.transcribeHTTP(ctx, audioPath, opts, model, format, diarize)
		if err != nil {
			return "", classifyError(err)
		}
		return result, nil
	}, isRetryableError)
}

// transcribeHTTP performs a transcription via direct HTTP to OpenAI's REST API.
func (t *OpenAITranscriber) transcribeHTTP(ctx context.Context, audioPath string, opts Options, model, format string, diarize bool) (_ string, err error) {
	// Open audio file
	file, err := os.Open(audioPath) // #nosec G304 -- audioPath is from internal chunking
	if err != nil {
		return "", fmt.Errorf("failed to open audio file: %w", err)
	}
	defer func() { _ = file.Close() }()

	// Build multipart form
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Add file field
	part, err := writer.CreateFormFile("file", filepath.Base(audioPath))
	if err != nil {
		return "", fmt.Errorf("failed to create form file: %w", err)
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("failed to copy file to form: %w", err)
	}

	// Add required fields
	if err := writer.WriteField("model", model); err != nil {
		return "", fmt.Errorf("failed to write model field: %w", err)
	}
	if err := writer.WriteField("response_format", format); err != nil {
		return "", fmt.Errorf("failed to write response_format field: %w", err)
	}

	// Diarization requires chunking_strategy
	if diarize {
		if err := writer.WriteField("chunking_strategy", ChunkingStrategyAuto); err != nil {
			return "", fmt.Errorf("failed to write chunking_strategy field: %w", err)
		}
	}

	// Add optional fields
	if opts.Prompt != "" {
		if err := writer.WriteField("prompt", opts.Prompt); err != nil {
			return "", fmt.Errorf("failed to write prompt field: %w", err)
		}
	}
	if langCode := opts.Language.BaseCode(); langCode != "" {
		if err := writer.WriteField("language", langCode); err != nil {
			return "", fmt.Errorf("failed to write language field: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Create HTTP request
	url := t.baseURL + transcriptionPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+t.apiKey)

	// Execute request
	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close response body: %w", closeErr)
		}
	}()

	// Read response body with size limit
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return "", fmt.Errorf("failed to read response: %w", err)
	}

	// Handle errors
	if resp.StatusCode != http.StatusOK {
		return "", parseHTTPError(resp.StatusCode, respBody)
	}

	// Parse response based on format
	if diarize {
		return parseDiarizeResponse(respBody)
	}
	return parseTranscriptionResponse(respBody)
}

// transcriptionResponse represents a standard OpenAI transcription JSON response.
type transcriptionResponse struct {
	Text string `json:"text"`
}

// parseTranscriptionResponse parses a standard transcription JSON response.
func parseTranscriptionResponse(body []byte) (string, error) {
	var resp transcriptionResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}
	return resp.Text, nil
}

// diarizeResponse represents the OpenAI diarized transcription response.
type diarizeResponse struct {
	Text     string `json:"text"`
	Segments []struct {
		ID      string  `json:"id"`
		Start   float64 `json:"start"`
		End     float64 `json:"end"`
		Text    string  `json:"text"`
		Speaker string  `json:"speaker"`
	} `json:"segments"`
}

// parseDiarizeResponse parses the diarized JSON response.
func parseDiarizeResponse(body []byte) (string, error) {
	var resp diarizeResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", fmt.Errorf("failed to parse response: %w", err)
	}

	// If no segments, return plain text
	if len(resp.Segments) == 0 {
		return resp.Text, nil
	}

	// Format with speaker labels
	var b strings.Builder
	for _, seg := range resp.Segments {
		speaker := seg.Speaker
		if speaker == "" {
			speaker = fmt.Sprintf("Speaker %s", seg.ID)
		}
		fmt.Fprintf(&b, "[%s] %s\n", speaker, strings.TrimSpace(seg.Text))
	}
	return strings.TrimSpace(b.String()), nil
}

// openAIAPIError represents an error response from OpenAI's REST API.
// Unexported: only used for error classification.
type openAIAPIError struct {
	StatusCode int
	Message    string
	Type       string
	Code       string
}

func (e *openAIAPIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("OpenAI API error %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("OpenAI API error %d", e.StatusCode)
}

// openAIErrorResponse represents the JSON error response envelope from OpenAI.
type openAIErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// parseHTTPError parses an HTTP error response from OpenAI into a typed error.
func parseHTTPError(statusCode int, body []byte) *openAIAPIError {
	var errResp openAIErrorResponse
	if err := json.Unmarshal(body, &errResp); err != nil {
		return &openAIAPIError{
			StatusCode: statusCode,
			Message:    string(body),
		}
	}

	return &openAIAPIError{
		StatusCode: statusCode,
		Message:    errResp.Error.Message,
		Type:       errResp.Error.Type,
		Code:       errResp.Error.Code,
	}
}

// classifyError maps OpenAI API errors to apierr sentinel errors.
func classifyError(err error) error {
	if err == nil {
		return nil
	}

	var apiErr *openAIAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusTooManyRequests:
			// Distinguish between temporary rate limit and quota exceeded (billing issue).
			if strings.Contains(apiErr.Message, "quota") ||
				strings.Contains(apiErr.Message, "billing") {
				return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrQuotaExceeded)
			}
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrRateLimit)
		case http.StatusPaymentRequired:
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrQuotaExceeded)
		case http.StatusUnauthorized:
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrAuthFailed)
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrTimeout)
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound:
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrBadRequest)
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrTimeout) // Retryable server error
		}
	}

	// Check for context timeout/deadline exceeded.
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("request timed out: %w", apierr.ErrTimeout)
	}

	return err
}

// isRetryableError determines if an error is transient and should be retried.
func isRetryableError(err error) bool {
	// Rate limits are retryable (with backoff).
	if errors.Is(err, apierr.ErrRateLimit) {
		return true
	}

	// Timeouts are retryable.
	if errors.Is(err, apierr.ErrTimeout) {
		return true
	}

	// Context cancellation is not retryable.
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Auth errors are not retryable.
	if errors.Is(err, apierr.ErrAuthFailed) {
		return false
	}

	return false
}

// TranscribeAll transcribes multiple audio chunks in parallel.
// Results are returned in the same order as the input chunks.
// If any chunk fails, the entire operation is aborted and the error is returned.
// maxParallel limits the number of concurrent API requests (1-MaxRecommendedParallel recommended).
func TranscribeAll(
	ctx context.Context,
	chunks []audio.Chunk,
	t Transcriber,
	opts Options,
	maxParallel int,
) ([]string, error) {
	if len(chunks) == 0 {
		return nil, nil
	}

	if maxParallel < 1 {
		maxParallel = 1
	}

	results := make([]string, len(chunks))
	// Semaphore channel for concurrency control.
	// Not closed explicitly: it's local to this function and will be GC'd.
	sem := make(chan struct{}, maxParallel)

	g, ctx := errgroup.WithContext(ctx)

	for i, chunk := range chunks {
		g.Go(func() error {
			// Acquire semaphore slot.
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			defer func() { <-sem }()

			text, err := t.Transcribe(ctx, chunk.Path, opts)
			if err != nil {
				return fmt.Errorf("chunk %d (%s): %w", chunk.Index, filepath.Base(chunk.Path), err)
			}
			results[i] = text
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	return results, nil
}
