package restructure_test

// Notes:
// - OpenAI-specific tests for OpenAIRestructurer
// - Tests use black-box approach via package restructure_test
// - Uses httptest.Server to mock OpenAI API responses (same pattern as DeepSeek tests)
// - Shared mocks are defined in restructurer_test.go

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alnah/transcript/internal/apierr"
	"github.com/alnah/transcript/internal/lang"
	"github.com/alnah/transcript/internal/restructure"
	"github.com/alnah/transcript/internal/template"
)

// ---------------------------------------------------------------------------
// Helpers - OpenAI mock server
// ---------------------------------------------------------------------------

// openAIResponse creates a mock OpenAI chat completion response.
func openAIResponse(content string) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1234567890,
		"model":   "o4-mini",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     100,
			"completion_tokens": 50,
			"total_tokens":      150,
		},
	}
}

// openAIErrorResponse creates a mock OpenAI error response.
func openAIErrorResponse(message, errType string) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
		},
	}
}

// mockOpenAIServer creates a test server that returns predefined OpenAI chat completion responses.
type mockOpenAIServer struct {
	*httptest.Server
	mu          sync.Mutex
	calls       []openAICall
	responses   []mockOpenAIResp
	responseIdx int
}

type openAICall struct {
	Model    string
	Messages []map[string]string
}

type mockOpenAIResp struct {
	statusCode int
	body       any
}

func newMockOpenAIServer() *mockOpenAIServer {
	m := &mockOpenAIServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()

		var req struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		messages := make([]map[string]string, len(req.Messages))
		for i, msg := range req.Messages {
			messages[i] = map[string]string{
				"role":    msg.Role,
				"content": msg.Content,
			}
		}
		m.calls = append(m.calls, openAICall{
			Model:    req.Model,
			Messages: messages,
		})

		var resp mockOpenAIResp
		if m.responseIdx < len(m.responses) {
			resp = m.responses[m.responseIdx]
			m.responseIdx++
		} else if len(m.responses) > 0 {
			resp = m.responses[len(m.responses)-1]
		} else {
			resp = mockOpenAIResp{
				statusCode: http.StatusOK,
				body:       openAIResponse("Default response"),
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.statusCode)
		json.NewEncoder(w).Encode(resp.body)
	}))
	return m
}

// newMockOpenAIServerWithHandler creates a test server with a custom handler.
// Useful for tests that need fine-grained control (e.g., cancelling context on request).
func newMockOpenAIServerWithHandler(handler http.HandlerFunc) *httptest.Server {
	return httptest.NewServer(handler)
}

func (m *mockOpenAIServer) addResponse(statusCode int, body any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, mockOpenAIResp{statusCode, body})
}

func (m *mockOpenAIServer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockOpenAIServer) lastCall() openAICall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return openAICall{}
	}
	return m.calls[len(m.calls)-1]
}

func (m *mockOpenAIServer) systemPrompt() string {
	call := m.lastCall()
	for _, msg := range call.Messages {
		if msg["role"] == "system" {
			return msg["content"]
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// TestClassifyRestructureError - OpenAI error classification
// ---------------------------------------------------------------------------

func TestClassifyRestructureError(t *testing.T) {
	t.Parallel()

	// Test nil error
	t.Run("nil error returns nil", func(t *testing.T) {
		t.Parallel()

		got := restructure.ClassifyRestructureError(nil)
		if got != nil {
			t.Errorf("ClassifyRestructureError(nil) = %v, want nil", got)
		}
	})

	// Test context deadline exceeded
	t.Run("context deadline exceeded returns ErrTimeout", func(t *testing.T) {
		t.Parallel()

		got := restructure.ClassifyRestructureError(context.DeadlineExceeded)
		if !errors.Is(got, apierr.ErrTimeout) {
			t.Errorf("ClassifyRestructureError(DeadlineExceeded) = %v, want ErrTimeout", got)
		}
	})

	// Test context length in plain error string
	t.Run("context length in plain error string returns ErrTranscriptTooLong", func(t *testing.T) {
		t.Parallel()

		got := restructure.ClassifyRestructureError(errors.New("context_length_exceeded"))
		if !errors.Is(got, restructure.ErrTranscriptTooLong) {
			t.Errorf("ClassifyRestructureError(context_length_exceeded) = %v, want ErrTranscriptTooLong", got)
		}
	})

	t.Run("maximum context length in plain error string returns ErrTranscriptTooLong", func(t *testing.T) {
		t.Parallel()

		got := restructure.ClassifyRestructureError(errors.New("maximum context length exceeded"))
		if !errors.Is(got, restructure.ErrTranscriptTooLong) {
			t.Errorf("ClassifyRestructureError(maximum context length) = %v, want ErrTranscriptTooLong", got)
		}
	})

	t.Run("unknown error passes through", func(t *testing.T) {
		t.Parallel()

		original := errors.New("random error")
		got := restructure.ClassifyRestructureError(original)
		if got != original {
			t.Errorf("ClassifyRestructureError(random) = %v, want original error", got)
		}
	})
}

// ---------------------------------------------------------------------------
// TestClassifyRestructureError_ViaHTTP - HTTP-level error classification
// ---------------------------------------------------------------------------

func TestClassifyRestructureError_ViaHTTP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       any
		wantErr    error
	}{
		{
			name:       "rate limit 429",
			statusCode: http.StatusTooManyRequests,
			body:       openAIErrorResponse("rate limit exceeded", "rate_limit_error"),
			wantErr:    apierr.ErrRateLimit,
		},
		{
			name:       "auth failed 401",
			statusCode: http.StatusUnauthorized,
			body:       openAIErrorResponse("invalid api key", "invalid_api_key"),
			wantErr:    apierr.ErrAuthFailed,
		},
		{
			name:       "request timeout 408",
			statusCode: http.StatusRequestTimeout,
			body:       openAIErrorResponse("request timed out", "timeout"),
			wantErr:    apierr.ErrTimeout,
		},
		{
			name:       "gateway timeout 504",
			statusCode: http.StatusGatewayTimeout,
			body:       openAIErrorResponse("gateway timeout", "timeout"),
			wantErr:    apierr.ErrTimeout,
		},
		{
			name:       "context length exceeded via status 400",
			statusCode: http.StatusBadRequest,
			body:       openAIErrorResponse("maximum context length exceeded", "invalid_request_error"),
			wantErr:    restructure.ErrTranscriptTooLong,
		},
		{
			name:       "context length exceeded via message pattern",
			statusCode: http.StatusBadRequest,
			body:       openAIErrorResponse("context_length issue", "invalid_request_error"),
			wantErr:    restructure.ErrTranscriptTooLong,
		},
		{
			name:       "server error 500",
			statusCode: http.StatusInternalServerError,
			body:       openAIErrorResponse("internal error", "server_error"),
			wantErr:    apierr.ErrTimeout, // 500 mapped to ErrTimeout for retry
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := newMockOpenAIServer()
			t.Cleanup(server.Close)

			server.addResponse(tt.statusCode, tt.body)

			r := restructure.NewOpenAIRestructurer("test-key",
				restructure.WithBaseURL(server.URL),
				restructure.WithMaxRetries(0),
			)

			_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Errorf("Restructure() error = %v, want error wrapping %v", err, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestIsRetryableRestructureError - OpenAI retry decision
// ---------------------------------------------------------------------------

func TestIsRetryableRestructureError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "rate limit is retryable",
			err:  fmt.Errorf("wrapped: %w", apierr.ErrRateLimit),
			want: true,
		},
		{
			name: "timeout is retryable",
			err:  fmt.Errorf("wrapped: %w", apierr.ErrTimeout),
			want: true,
		},
		{
			name: "context canceled is not retryable",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "auth failed is not retryable",
			err:  fmt.Errorf("wrapped: %w", apierr.ErrAuthFailed),
			want: false,
		},
		{
			name: "transcript too long is not retryable",
			err:  fmt.Errorf("wrapped: %w", restructure.ErrTranscriptTooLong),
			want: false,
		},
		{
			name: "unknown error is not retryable",
			err:  errors.New("random error"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := restructure.IsRetryableRestructureError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableRestructureError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestOpenAIRestructurer_Restructure - OpenAI restructuring
// ---------------------------------------------------------------------------

func TestOpenAIRestructurer_Restructure(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns restructured content", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, openAIResponse("# Restructured Content\n\nThis is the result."))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		result, err := r.Restructure(context.Background(), "Raw transcript.", template.MustParseName("meeting"), lang.Language{})
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		want := "# Restructured Content\n\nThis is the result."
		if result != want {
			t.Errorf("Restructure() = %q, want %q", result, want)
		}

		if got, want := server.callCount(), 1; got != want {
			t.Errorf("callCount() = %d, want %d", got, want)
		}
	})

	t.Run("transcript too long returns error", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithMaxInputTokens(10),
		)

		longTranscript := strings.Repeat("x", 100)

		_, err := r.Restructure(context.Background(), longTranscript, template.MustParseName("meeting"), lang.Language{})
		if err == nil {
			t.Fatal("Restructure() with long transcript: got nil error, want ErrTranscriptTooLong")
		}

		if !errors.Is(err, restructure.ErrTranscriptTooLong) {
			t.Errorf("Restructure() error = %v, want ErrTranscriptTooLong", err)
		}

		if got := server.callCount(); got != 0 {
			t.Errorf("callCount() = %d, want 0 (should not call API if transcript too long)", got)
		}
	})

	t.Run("adds language instruction for non-English", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, openAIResponse("Contenu restructure."))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.MustParse("fr"))
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		prompt := server.systemPrompt()
		if !strings.Contains(prompt, "Respond in French") {
			t.Errorf("systemPrompt() = %q, want containing %q", prompt, "Respond in French")
		}
	})

	t.Run("no language instruction for English", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, openAIResponse("Restructured content."))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.MustParse("en"))
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		prompt := server.systemPrompt()
		if strings.Contains(prompt, "Respond in") {
			t.Errorf("systemPrompt() = %q, should not contain language instruction for English", prompt)
		}
	})

	t.Run("no language instruction for en-US", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, openAIResponse("Restructured content."))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.MustParse("en-US"))
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		prompt := server.systemPrompt()
		if strings.Contains(prompt, "Respond in") {
			t.Errorf("systemPrompt() = %q, should not contain language instruction for en-US", prompt)
		}
	})

	t.Run("no language instruction for empty outputLang", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, openAIResponse("Content."))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		prompt := server.systemPrompt()
		if strings.Contains(prompt, "Respond in") {
			t.Errorf("systemPrompt() = %q, should not contain language instruction for empty lang", prompt)
		}
	})

	t.Run("API returns empty choices", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, map[string]any{
			"id":      "test",
			"choices": []any{},
		})

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithMaxRetries(0),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err == nil {
			t.Fatal("Restructure() with empty choices: got nil error, want non-nil")
		}

		if !strings.Contains(err.Error(), "no response") {
			t.Errorf("Restructure() error = %q, want containing %q", err.Error(), "no response")
		}
	})
}

// ---------------------------------------------------------------------------
// TestOpenAIRetryBehavior - OpenAI retry with backoff
// ---------------------------------------------------------------------------

func TestOpenAIRetryBehavior(t *testing.T) {
	t.Parallel()

	t.Run("retries on rate limit then succeeds", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusTooManyRequests, openAIErrorResponse("rate limit", "rate_limit_error"))
		server.addResponse(http.StatusTooManyRequests, openAIErrorResponse("rate limit", "rate_limit_error"))
		server.addResponse(http.StatusOK, openAIResponse("Success after retries"))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithMaxRetries(5),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		result, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		want := "Success after retries"
		if result != want {
			t.Errorf("Restructure() = %q, want %q", result, want)
		}

		if got, want := server.callCount(), 3; got != want {
			t.Errorf("callCount() = %d, want %d", got, want)
		}
	})

	t.Run("does not retry on auth error", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusUnauthorized, openAIErrorResponse("invalid key", "invalid_api_key"))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithMaxRetries(5),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err == nil {
			t.Fatal("Restructure() with auth error: got nil error, want ErrAuthFailed")
		}

		if !errors.Is(err, apierr.ErrAuthFailed) {
			t.Errorf("Restructure() error = %v, want ErrAuthFailed", err)
		}

		if got, want := server.callCount(), 1; got != want {
			t.Errorf("callCount() = %d, want %d (no retry)", got, want)
		}
	})

	t.Run("does not retry on transcript too long", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusBadRequest, openAIErrorResponse("maximum context length exceeded", "invalid_request_error"))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithMaxRetries(5),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err == nil {
			t.Fatal("Restructure() with context length error: got nil error, want ErrTranscriptTooLong")
		}

		if !errors.Is(err, restructure.ErrTranscriptTooLong) {
			t.Errorf("Restructure() error = %v, want ErrTranscriptTooLong", err)
		}

		if got, want := server.callCount(), 1; got != want {
			t.Errorf("callCount() = %d, want %d (no retry)", got, want)
		}
	})

	t.Run("max retries exceeded", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		// All responses are rate limit errors
		server.addResponse(http.StatusTooManyRequests, openAIErrorResponse("rate limit", "rate_limit_error"))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithMaxRetries(2),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err == nil {
			t.Fatal("Restructure() after max retries: got nil error, want non-nil")
		}

		if !strings.Contains(err.Error(), "max retries") {
			t.Errorf("Restructure() error = %q, want containing %q", err.Error(), "max retries")
		}

		if got, want := server.callCount(), 3; got != want {
			t.Errorf("callCount() = %d, want %d", got, want)
		}
	})

	t.Run("retries on server error 500", func(t *testing.T) {
		t.Parallel()

		server := newMockOpenAIServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusInternalServerError, openAIErrorResponse("server error", "server_error"))
		server.addResponse(http.StatusOK, openAIResponse("Success"))

		r := restructure.NewOpenAIRestructurer("test-key",
			restructure.WithBaseURL(server.URL),
			restructure.WithMaxRetries(3),
			restructure.WithRetryDelays(time.Millisecond, time.Millisecond),
		)

		result, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		want := "Success"
		if result != want {
			t.Errorf("Restructure() = %q, want %q", result, want)
		}

		if got, want := server.callCount(), 2; got != want {
			t.Errorf("callCount() = %d, want %d", got, want)
		}
	})
}
