package restructure

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/alnah/transcript/internal/apierr"
	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/template"
)

// DeepSeek API configuration.
const (
	// API endpoint
	defaultDeepSeekBaseURL = "https://api.deepseek.com"

	// Model configuration
	defaultDeepSeekModel           = "deepseek-reasoner" // 64K max output, thinking mode
	defaultDeepSeekMaxInputTokens  = 100000              // Conservative limit (128K context)
	defaultDeepSeekMaxOutputTokens = 64000               // deepseek-reasoner max

	// Retry configuration
	defaultDeepSeekMaxRetries  = 3
	defaultDeepSeekBaseDelay   = 1 * time.Second
	defaultDeepSeekMaxDelay    = 30 * time.Second
	defaultDeepSeekHTTPTimeout = 10 * time.Minute // Long timeout for large transcripts

	// Response size limit to prevent OOM from malformed responses (10MB)
	maxResponseSize = 10 * 1024 * 1024
)

// httpDoer abstracts HTTP client for testing.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Compile-time interface compliance check.
var _ Restructurer = (*DeepSeekRestructurer)(nil)

// DeepSeekRestructurer restructures transcripts using DeepSeek's chat completion API.
// It supports automatic retries with exponential backoff for transient errors.
type DeepSeekRestructurer struct {
	apiKey          string
	baseURL         string
	model           string
	maxInputTokens  int
	maxOutputTokens int
	maxRetries      int
	baseDelay       time.Duration
	maxDelay        time.Duration
	httpTimeout     time.Duration
	httpClient      httpDoer
}

// DeepSeekOption configures a DeepSeekRestructurer.
type DeepSeekOption func(*DeepSeekRestructurer)

// WithDeepSeekModel sets the model for restructuring.
// Available: "deepseek-reasoner" (64K output), "deepseek-chat" (8K output).
func WithDeepSeekModel(model string) DeepSeekOption {
	return func(r *DeepSeekRestructurer) {
		r.model = model
	}
}

// WithDeepSeekMaxInputTokens sets the maximum input token limit.
func WithDeepSeekMaxInputTokens(max int) DeepSeekOption {
	return func(r *DeepSeekRestructurer) {
		if max > 0 {
			r.maxInputTokens = max
		}
	}
}

// WithDeepSeekMaxOutputTokens sets the maximum output token limit.
func WithDeepSeekMaxOutputTokens(max int) DeepSeekOption {
	return func(r *DeepSeekRestructurer) {
		if max > 0 {
			r.maxOutputTokens = max
		}
	}
}

// WithDeepSeekMaxRetries sets the maximum number of retry attempts.
func WithDeepSeekMaxRetries(n int) DeepSeekOption {
	return func(r *DeepSeekRestructurer) {
		if n >= 0 {
			r.maxRetries = n
		}
	}
}

// WithDeepSeekRetryDelays sets the base and max delays for exponential backoff.
func WithDeepSeekRetryDelays(base, max time.Duration) DeepSeekOption {
	return func(r *DeepSeekRestructurer) {
		if base > 0 {
			r.baseDelay = base
		}
		if max > 0 {
			r.maxDelay = max
		}
	}
}

// WithDeepSeekBaseURL sets a custom base URL (for testing or proxies).
func WithDeepSeekBaseURL(url string) DeepSeekOption {
	return func(r *DeepSeekRestructurer) {
		r.baseURL = strings.TrimSuffix(url, "/")
	}
}

// WithDeepSeekHTTPTimeout sets the HTTP client timeout.
// Default is 5 minutes to accommodate large transcript processing.
func WithDeepSeekHTTPTimeout(timeout time.Duration) DeepSeekOption {
	return func(r *DeepSeekRestructurer) {
		if timeout > 0 {
			r.httpTimeout = timeout
		}
	}
}

// withDeepSeekHTTPClient sets a custom HTTP client (for testing).
func withDeepSeekHTTPClient(client httpDoer) DeepSeekOption {
	return func(r *DeepSeekRestructurer) {
		r.httpClient = client
	}
}

// NewDeepSeekRestructurer creates a new DeepSeekRestructurer.
// apiKey is required and must be a valid DeepSeek API key.
// Returns nil and ErrEmptyAPIKey if apiKey is empty.
func NewDeepSeekRestructurer(apiKey string, opts ...DeepSeekOption) (*DeepSeekRestructurer, error) {
	if apiKey == "" {
		return nil, ErrEmptyAPIKey
	}

	r := &DeepSeekRestructurer{
		apiKey:          apiKey,
		baseURL:         defaultDeepSeekBaseURL,
		model:           defaultDeepSeekModel,
		maxInputTokens:  defaultDeepSeekMaxInputTokens,
		maxOutputTokens: defaultDeepSeekMaxOutputTokens,
		maxRetries:      defaultDeepSeekMaxRetries,
		baseDelay:       defaultDeepSeekBaseDelay,
		maxDelay:        defaultDeepSeekMaxDelay,
		httpTimeout:     defaultDeepSeekHTTPTimeout,
	}
	for _, opt := range opts {
		opt(r)
	}
	// Create HTTP client after options are applied (timeout may be customized)
	if r.httpClient == nil {
		r.httpClient = &http.Client{Timeout: r.httpTimeout}
	}
	return r, nil
}

// Restructure transforms a raw transcript into structured markdown using the specified template.
// outputLang specifies the output language. Zero value uses template's native language (English).
// Returns ErrTranscriptTooLong if the transcript exceeds the token limit (estimated).
// Automatically retries on transient errors (rate limits, timeouts, server errors).
func (r *DeepSeekRestructurer) Restructure(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, error) {
	// 1. Get prompt from validated template
	prompt := tmpl.Prompt()

	// 2. Add language instruction if output is not English
	if !outputLang.IsZero() && !outputLang.IsEnglish() {
		prompt = fmt.Sprintf("Respond in %s.\n\n%s", outputLang.DisplayName(), prompt)
	}

	// 3. Estimate tokens and check limit
	estimatedTokens := estimateTokens(transcript)
	if estimatedTokens > r.maxInputTokens {
		return "", fmt.Errorf("transcript too long (%dK tokens estimated, max %dK): %w",
			estimatedTokens/1000, r.maxInputTokens/1000, ErrTranscriptTooLong)
	}

	// 4. Build request
	req := deepSeekRequest{
		Model:       r.model,
		MaxTokens:   r.maxOutputTokens,
		Temperature: 0, // Deterministic output
		Messages: []deepSeekMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: transcript},
		},
	}

	// 5. Call API with retry
	return r.restructureWithRetry(ctx, req)
}

// RestructureWithCustomPrompt executes restructuring with a custom prompt (used by MapReduce).
// Unlike Restructure, this does not resolve templates or check token limits.
func (r *DeepSeekRestructurer) RestructureWithCustomPrompt(ctx context.Context, content, prompt string) (string, error) {
	req := deepSeekRequest{
		Model:       r.model,
		MaxTokens:   r.maxOutputTokens,
		Temperature: 0,
		Messages: []deepSeekMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: content},
		},
	}
	return r.restructureWithRetry(ctx, req)
}

// restructureWithRetry executes the restructuring with exponential backoff retry.
func (r *DeepSeekRestructurer) restructureWithRetry(ctx context.Context, req deepSeekRequest) (string, error) {
	cfg := apierr.RetryConfig{
		MaxRetries: r.maxRetries,
		BaseDelay:  r.baseDelay,
		MaxDelay:   r.maxDelay,
	}

	return apierr.RetryWithBackoff(ctx, cfg, func() (string, error) {
		resp, err := r.callAPI(ctx, req)
		if err != nil {
			return "", classifyDeepSeekError(err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response from DeepSeek API")
		}
		return resp.Choices[0].Message.Content, nil
	}, isRetryableDeepSeekError)
}

// deepSeekRequest represents a DeepSeek chat completion request.
type deepSeekRequest struct {
	Model       string            `json:"model"`
	Messages    []deepSeekMessage `json:"messages"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Temperature float64           `json:"temperature"` // 0 for deterministic output
}

// deepSeekMessage represents a message in the conversation.
type deepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// deepSeekResponse represents a DeepSeek chat completion response.
type deepSeekResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// deepSeekErrorResponse represents an error response from the DeepSeek API.
type deepSeekErrorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// callAPI makes an HTTP request to the DeepSeek API.
func (r *DeepSeekRestructurer) callAPI(ctx context.Context, reqBody deepSeekRequest) (_ *deepSeekResponse, err error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := r.baseURL + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.apiKey)

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("failed to close response body: %w", closeErr)
		}
	}()

	// Limit response size to prevent OOM from malformed responses
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize))
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, parseDeepSeekError(resp.StatusCode, respBody)
	}

	var result deepSeekResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

// deepSeekAPIError represents a typed DeepSeek API error.
type deepSeekAPIError struct {
	StatusCode int
	Message    string
	Type       string
	Code       string
}

func (e *deepSeekAPIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("DeepSeek API error %d: %s", e.StatusCode, e.Message)
	}
	return fmt.Sprintf("DeepSeek API error %d", e.StatusCode)
}

// parseDeepSeekError parses an error response from the DeepSeek API.
func parseDeepSeekError(statusCode int, body []byte) *deepSeekAPIError {
	var errResp deepSeekErrorResponse
	if err := json.Unmarshal(body, &errResp); err != nil {
		// If we can't parse the error, return a generic error
		return &deepSeekAPIError{
			StatusCode: statusCode,
			Message:    string(body),
		}
	}

	return &deepSeekAPIError{
		StatusCode: statusCode,
		Message:    errResp.Error.Message,
		Type:       errResp.Error.Type,
		Code:       errResp.Error.Code,
	}
}

// classifyDeepSeekError maps DeepSeek API errors to sentinel errors.
func classifyDeepSeekError(err error) error {
	if err == nil {
		return nil
	}

	var apiErr *deepSeekAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusTooManyRequests: // 429
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrRateLimit)
		case http.StatusUnauthorized: // 401
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrAuthFailed)
		case http.StatusPaymentRequired: // 402 - DeepSeek uses this for insufficient balance
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrQuotaExceeded)
		case http.StatusRequestTimeout, http.StatusGatewayTimeout: // 408, 504
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrTimeout)
		case http.StatusBadRequest: // 400
			// Check for context length exceeded
			if strings.Contains(apiErr.Message, "context_length") ||
				strings.Contains(apiErr.Message, "maximum context length") ||
				strings.Contains(apiErr.Message, "too long") {
				return fmt.Errorf("API rejected: %w", ErrTranscriptTooLong)
			}
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrBadRequest)
		case http.StatusUnprocessableEntity: // 422 - Invalid parameters
			if strings.Contains(apiErr.Message, "context") ||
				strings.Contains(apiErr.Message, "length") ||
				strings.Contains(apiErr.Message, "token") {
				return fmt.Errorf("API rejected: %w", ErrTranscriptTooLong)
			}
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrBadRequest)
		}
	}

	// Check for context timeout/deadline exceeded
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("request timed out: %w", apierr.ErrTimeout)
	}

	return err
}

// isRetryableDeepSeekError determines if an error is transient and should be retried.
func isRetryableDeepSeekError(err error) bool {
	// Rate limits are retryable (with backoff)
	if errors.Is(err, apierr.ErrRateLimit) {
		return true
	}

	// Timeouts are retryable
	if errors.Is(err, apierr.ErrTimeout) {
		return true
	}

	// Server errors (5xx) are retryable
	var apiErr *deepSeekAPIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusInternalServerError, // 500
			http.StatusBadGateway,         // 502
			http.StatusServiceUnavailable, // 503
			http.StatusGatewayTimeout:     // 504
			return true
		}
	}

	// Context cancellation is not retryable
	if errors.Is(err, context.Canceled) {
		return false
	}

	// Auth errors are not retryable
	if errors.Is(err, apierr.ErrAuthFailed) {
		return false
	}

	// Quota exceeded is not retryable
	if errors.Is(err, apierr.ErrQuotaExceeded) {
		return false
	}

	// Transcript too long is not retryable
	if errors.Is(err, ErrTranscriptTooLong) {
		return false
	}

	return false
}
