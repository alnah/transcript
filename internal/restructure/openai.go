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

// OpenAI API configuration.
const (
	// Default base URL for the OpenAI API.
	defaultOpenAIBaseURL = "https://api.openai.com"

	// Model configuration.
	defaultRestructureModel = "o4-mini"
	defaultMaxInputTokens   = 100000
	defaultMaxOutputTokens  = 100000 // o4-mini max output tokens

	// Retry configuration: fewer retries than transcriber (longer latency).
	defaultRestructureMaxRetries = 3
	defaultRestructureBaseDelay  = 1 * time.Second
	defaultRestructureMaxDelay   = 30 * time.Second

	// HTTP timeout for OpenAI chat completion requests.
	defaultOpenAIHTTPTimeout = 10 * time.Minute
)

// Compile-time interface compliance check.
var _ Restructurer = (*OpenAIRestructurer)(nil)

// OpenAIRestructurer restructures transcripts using OpenAI's chat completion REST API.
// It supports automatic retries with exponential backoff for transient errors.
type OpenAIRestructurer struct {
	apiKey         string
	baseURL        string
	model          string
	maxInputTokens int
	maxRetries     int
	baseDelay      time.Duration
	maxDelay       time.Duration
	httpTimeout    time.Duration
	httpClient     httpDoer
}

// Option configures an OpenAIRestructurer.
type Option func(*OpenAIRestructurer)

// WithModel sets the model for restructuring.
func WithModel(model string) Option {
	return func(r *OpenAIRestructurer) {
		r.model = model
	}
}

// WithMaxInputTokens sets the maximum input token limit.
func WithMaxInputTokens(max int) Option {
	return func(r *OpenAIRestructurer) {
		r.maxInputTokens = max
	}
}

// WithMaxRetries sets the maximum number of retry attempts.
func WithMaxRetries(n int) Option {
	return func(r *OpenAIRestructurer) {
		if n >= 0 {
			r.maxRetries = n
		}
	}
}

// WithRetryDelays sets the base and max delays for exponential backoff.
func WithRetryDelays(base, max time.Duration) Option {
	return func(r *OpenAIRestructurer) {
		if base > 0 {
			r.baseDelay = base
		}
		if max > 0 {
			r.maxDelay = max
		}
	}
}

// WithBaseURL sets a custom base URL (for testing or proxies).
func WithBaseURL(url string) Option {
	return func(r *OpenAIRestructurer) {
		r.baseURL = strings.TrimSuffix(url, "/")
	}
}

// WithHTTPClient sets a custom HTTP client (for testing).
func WithHTTPClient(c httpDoer) Option {
	return func(r *OpenAIRestructurer) {
		r.httpClient = c
	}
}

// NewOpenAIRestructurer creates a new OpenAIRestructurer.
// apiKey is required. Use options to customize model, token limits, and retry behavior.
func NewOpenAIRestructurer(apiKey string, opts ...Option) *OpenAIRestructurer {
	r := &OpenAIRestructurer{
		apiKey:         apiKey,
		baseURL:        defaultOpenAIBaseURL,
		model:          defaultRestructureModel,
		maxInputTokens: defaultMaxInputTokens,
		maxRetries:     defaultRestructureMaxRetries,
		baseDelay:      defaultRestructureBaseDelay,
		maxDelay:       defaultRestructureMaxDelay,
		httpTimeout:    defaultOpenAIHTTPTimeout,
	}
	for _, opt := range opts {
		opt(r)
	}
	// Create HTTP client after options are applied (timeout may be customized).
	if r.httpClient == nil {
		r.httpClient = &http.Client{Timeout: r.httpTimeout}
	}
	return r
}

// Restructure transforms a raw transcript into structured markdown using the specified template.
// outputLang specifies the output language. Zero value uses template's native language (English).
// Returns ErrTranscriptTooLong if the transcript exceeds the token limit (estimated).
// Automatically retries on transient errors (rate limits, timeouts, server errors).
//
// Token estimation uses len(text)/3 which is conservative for French text.
// The actual API limit is 128K tokens; we use 100K as a safety margin.
func (r *OpenAIRestructurer) Restructure(ctx context.Context, transcript string, tmpl template.Name, outputLang lang.Language) (string, error) {
	// 1. Get prompt from validated template
	prompt := tmpl.Prompt()

	// 2. Add language instruction if output is not English (template's native language)
	// English output (en, en-US, en-GB, etc.) skips this instruction since templates are native English.
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
	req := openAIRequest{
		Model:               r.model,
		MaxCompletionTokens: defaultMaxOutputTokens,
		Temperature:         0, // Deterministic output for reproducibility
		Messages: []openAIMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: transcript},
		},
	}

	// 5. Call API with retry
	return r.restructureWithRetry(ctx, req)
}

// RestructureWithCustomPrompt executes restructuring with a custom prompt (used by MapReduce).
// Unlike Restructure, this does not resolve templates or check token limits.
func (r *OpenAIRestructurer) RestructureWithCustomPrompt(ctx context.Context, content, prompt string) (string, error) {
	req := openAIRequest{
		Model:               r.model,
		MaxCompletionTokens: defaultMaxOutputTokens,
		Temperature:         0,
		Messages: []openAIMessage{
			{Role: "system", Content: prompt},
			{Role: "user", Content: content},
		},
	}
	return r.restructureWithRetry(ctx, req)
}

// restructureWithRetry executes the restructuring with exponential backoff retry.
func (r *OpenAIRestructurer) restructureWithRetry(ctx context.Context, req openAIRequest) (string, error) {
	cfg := apierr.RetryConfig{
		MaxRetries: r.maxRetries,
		BaseDelay:  r.baseDelay,
		MaxDelay:   r.maxDelay,
	}

	return apierr.RetryWithBackoff(ctx, cfg, func() (string, error) {
		resp, err := r.callAPI(ctx, req)
		if err != nil {
			return "", classifyRestructureError(err)
		}
		if len(resp.Choices) == 0 {
			return "", fmt.Errorf("no response from API")
		}
		return resp.Choices[0].Message.Content, nil
	}, isRetryableRestructureError)
}

// OpenAI chat completion request/response types.

// openAIRequest represents an OpenAI chat completion request.
type openAIRequest struct {
	Model               string          `json:"model"`
	Messages            []openAIMessage `json:"messages"`
	MaxCompletionTokens int             `json:"max_completion_tokens,omitempty"`
	Temperature         float64         `json:"temperature"`
}

// openAIMessage represents a message in the conversation.
type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openAIResponse represents an OpenAI chat completion response.
type openAIResponse struct {
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

// callAPI makes an HTTP request to the OpenAI chat completion API.
func (r *OpenAIRestructurer) callAPI(ctx context.Context, reqBody openAIRequest) (_ *openAIResponse, err error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	url := r.baseURL + "/v1/chat/completions"
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
		return nil, parseOpenAIError(resp.StatusCode, respBody)
	}

	var result openAIResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

// openAIAPIError represents a typed OpenAI API error.
// Unexported: only used for error classification within the restructure package.
type openAIAPIError struct {
	StatusCode int
	Message    string
	Type       string
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

// parseOpenAIError parses an error response from the OpenAI API.
func parseOpenAIError(statusCode int, body []byte) *openAIAPIError {
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
	}
}

// classifyRestructureError maps OpenAI API errors to apierr sentinel errors.
func classifyRestructureError(err error) error {
	if err == nil {
		return nil
	}

	// Check for typed API errors first (most reliable).
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
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrTimeout) // Retryable server error
		case http.StatusBadRequest:
			// Check for context length exceeded in message.
			if strings.Contains(apiErr.Message, "context_length") ||
				strings.Contains(apiErr.Message, "maximum context length") {
				return fmt.Errorf("API rejected: %w", ErrTranscriptTooLong)
			}
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrBadRequest)
		case http.StatusForbidden, http.StatusNotFound:
			return fmt.Errorf("%s: %w", apiErr.Message, apierr.ErrBadRequest)
		}
	}

	// Check for context timeout/deadline exceeded.
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("request timed out: %w", apierr.ErrTimeout)
	}

	// Fallback: check error message for context length (some errors may not be typed).
	errStr := err.Error()
	if strings.Contains(errStr, "context_length_exceeded") ||
		strings.Contains(errStr, "maximum context length") {
		return fmt.Errorf("API rejected: %w", ErrTranscriptTooLong)
	}

	return err
}

// isRetryableRestructureError determines if an error is transient and should be retried.
func isRetryableRestructureError(err error) bool {
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

	// Transcript too long is not retryable.
	if errors.Is(err, ErrTranscriptTooLong) {
		return false
	}

	return false
}
