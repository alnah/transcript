package restructure_test

// Notes:
// - Tests use black-box approach via package restructure_test
// - Internal functions are tested via export_test.go exports
// - Uses httptest.Server to mock DeepSeek API responses
// - Retry delays are set to 1ms to keep tests fast

import (
	"context"
	"encoding/json"
	"errors"
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
// Helpers - DeepSeek mock server
// ---------------------------------------------------------------------------

// deepSeekResponse creates a mock DeepSeek API response.
func deepSeekResponse(content string) map[string]any {
	return map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1234567890,
		"model":   "deepseek-reasoner",
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

// deepSeekErrorResponse creates a mock DeepSeek API error response.
func deepSeekErrorResponse(message, errType, code string) map[string]any {
	return map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"code":    code,
		},
	}
}

// mockDeepSeekServer creates a test server that returns predefined responses.
type mockDeepSeekServer struct {
	*httptest.Server
	mu          sync.Mutex
	calls       []deepSeekCall
	responses   []mockResponse
	responseIdx int
}

type deepSeekCall struct {
	Model    string
	Messages []map[string]string
}

type mockResponse struct {
	statusCode int
	body       any
}

func newMockDeepSeekServer() *mockDeepSeekServer {
	m := &mockDeepSeekServer{}
	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()

		// Parse request body
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

		// Record call
		messages := make([]map[string]string, len(req.Messages))
		for i, msg := range req.Messages {
			messages[i] = map[string]string{
				"role":    msg.Role,
				"content": msg.Content,
			}
		}
		m.calls = append(m.calls, deepSeekCall{
			Model:    req.Model,
			Messages: messages,
		})

		// Get response
		var resp mockResponse
		if m.responseIdx < len(m.responses) {
			resp = m.responses[m.responseIdx]
			m.responseIdx++
		} else if len(m.responses) > 0 {
			// Use last response if we've exhausted the sequence
			resp = m.responses[len(m.responses)-1]
		} else {
			// Default success response
			resp = mockResponse{
				statusCode: http.StatusOK,
				body:       deepSeekResponse("Default response"),
			}
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.statusCode)
		json.NewEncoder(w).Encode(resp.body)
	}))
	return m
}

func (m *mockDeepSeekServer) addResponse(statusCode int, body any) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.responses = append(m.responses, mockResponse{statusCode, body})
}

func (m *mockDeepSeekServer) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

func (m *mockDeepSeekServer) lastCall() deepSeekCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.calls) == 0 {
		return deepSeekCall{}
	}
	return m.calls[len(m.calls)-1]
}

func (m *mockDeepSeekServer) systemPrompt() string {
	call := m.lastCall()
	for _, msg := range call.Messages {
		if msg["role"] == "system" {
			return msg["content"]
		}
	}
	return ""
}

// mustNewDeepSeekRestructurer creates a DeepSeekRestructurer and fails the test if it errors.
func mustNewDeepSeekRestructurer(t *testing.T, apiKey string, opts ...restructure.DeepSeekOption) *restructure.DeepSeekRestructurer {
	t.Helper()
	r, err := restructure.NewDeepSeekRestructurer(apiKey, opts...)
	if err != nil {
		t.Fatalf("NewDeepSeekRestructurer failed: %v", err)
	}
	return r
}

// ---------------------------------------------------------------------------
// TestNewDeepSeekRestructurer - Constructor validation
// ---------------------------------------------------------------------------

func TestNewDeepSeekRestructurer(t *testing.T) {
	t.Parallel()

	t.Run("empty API key returns error", func(t *testing.T) {
		t.Parallel()

		_, err := restructure.NewDeepSeekRestructurer("")
		if err == nil {
			t.Fatal("NewDeepSeekRestructurer(\"\") expected error, got nil")
		}
		if !errors.Is(err, restructure.ErrEmptyAPIKey) {
			t.Errorf("NewDeepSeekRestructurer(\"\") error = %v, want ErrEmptyAPIKey", err)
		}
	})

	t.Run("valid API key succeeds", func(t *testing.T) {
		t.Parallel()

		r, err := restructure.NewDeepSeekRestructurer("test-key")
		if err != nil {
			t.Fatalf("NewDeepSeekRestructurer(\"test-key\") unexpected error: %v", err)
		}
		if r == nil {
			t.Fatal("NewDeepSeekRestructurer(\"test-key\") returned nil restructurer")
		}
	})

	t.Run("options with invalid values are ignored", func(t *testing.T) {
		t.Parallel()

		// These should not panic or error - invalid values should be ignored
		r, err := restructure.NewDeepSeekRestructurer("test-key",
			restructure.WithDeepSeekMaxInputTokens(0),  // Should be ignored
			restructure.WithDeepSeekMaxInputTokens(-1), // Should be ignored
			restructure.WithDeepSeekMaxOutputTokens(0), // Should be ignored
			restructure.WithDeepSeekMaxRetries(-1),     // Should be ignored (0 is valid)
		)
		if err != nil {
			t.Fatalf("NewDeepSeekRestructurer(\"test-key\", invalid_opts) unexpected error: %v", err)
		}
		if r == nil {
			t.Fatal("NewDeepSeekRestructurer(\"test-key\", invalid_opts) returned nil restructurer")
		}
	})
}

// ---------------------------------------------------------------------------
// TestClassifyDeepSeekError - Error classification
// ---------------------------------------------------------------------------

func TestClassifyDeepSeekError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		wantErr error
		wantNil bool
	}{
		{
			name:    "nil error returns nil",
			err:     nil,
			wantNil: true,
		},
		{
			name:    "context deadline exceeded",
			err:     context.DeadlineExceeded,
			wantErr: apierr.ErrTimeout,
		},
		{
			name:    "unknown error passes through",
			err:     errors.New("random error"),
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := restructure.ClassifyDeepSeekError(tt.err)

			if tt.wantNil {
				if got != nil {
					t.Errorf("ClassifyDeepSeekError(%v) = %v, want nil", tt.err, got)
				}
				return
			}

			if tt.wantErr == nil {
				if got == nil {
					t.Errorf("ClassifyDeepSeekError(%v) = nil, want non-nil error", tt.err)
				}
				return
			}

			if !errors.Is(got, tt.wantErr) {
				t.Errorf("ClassifyDeepSeekError(%v) = %v, want error wrapping %v", tt.err, got, tt.wantErr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestIsRetryableDeepSeekError - Retry decision
// ---------------------------------------------------------------------------

func TestIsRetryableDeepSeekError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "rate limit is retryable",
			err:  apierr.ErrRateLimit,
			want: true,
		},
		{
			name: "timeout is retryable",
			err:  apierr.ErrTimeout,
			want: true,
		},
		{
			name: "context canceled is not retryable",
			err:  context.Canceled,
			want: false,
		},
		{
			name: "auth failed is not retryable",
			err:  apierr.ErrAuthFailed,
			want: false,
		},
		{
			name: "quota exceeded is not retryable",
			err:  apierr.ErrQuotaExceeded,
			want: false,
		},
		{
			name: "transcript too long is not retryable",
			err:  restructure.ErrTranscriptTooLong,
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

			got := restructure.IsRetryableDeepSeekError(tt.err)
			if got != tt.want {
				t.Errorf("IsRetryableDeepSeekError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestDeepSeekRestructurer_Restructure - Main restructuring
// ---------------------------------------------------------------------------

func TestDeepSeekRestructurer_Restructure(t *testing.T) {
	t.Parallel()

	t.Run("happy path returns restructured content", func(t *testing.T) {
		t.Parallel()

		server := newMockDeepSeekServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, deepSeekResponse("# Restructured Content\n\nThis is the result."))

		r := mustNewDeepSeekRestructurer(t, "test-api-key",
			restructure.WithDeepSeekBaseURL(server.URL),
			restructure.WithDeepSeekRetryDelays(time.Millisecond, time.Millisecond),
		)

		result, err := r.Restructure(context.Background(), "Raw transcript.", template.MustParseName("meeting"), lang.Language{})
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		if result != "# Restructured Content\n\nThis is the result." {
			t.Errorf("Restructure() = %q, want %q", result, "# Restructured Content\n\nThis is the result.")
		}

		if server.callCount() != 1 {
			t.Errorf("callCount() = %d, want 1", server.callCount())
		}
	})

	// Note: "invalid template returns error" test removed.
	// With the template.Name type, invalid templates are caught at parse time
	// (template.ParseName), not at restructure time. This is tested in template_test.go.

	t.Run("transcript too long returns error", func(t *testing.T) {
		t.Parallel()

		server := newMockDeepSeekServer()
		t.Cleanup(server.Close)

		r := mustNewDeepSeekRestructurer(t, "test-api-key",
			restructure.WithDeepSeekBaseURL(server.URL),
			restructure.WithDeepSeekMaxInputTokens(10), // Very low limit
		)

		// Create transcript that exceeds 10 tokens (10*3=30 chars)
		longTranscript := strings.Repeat("x", 100)

		_, err := r.Restructure(context.Background(), longTranscript, template.MustParseName("meeting"), lang.Language{})
		if err == nil {
			t.Fatal("Restructure(long_transcript) expected error, got nil")
		}

		if !errors.Is(err, restructure.ErrTranscriptTooLong) {
			t.Errorf("Restructure(long_transcript) error = %v, want ErrTranscriptTooLong", err)
		}

		if server.callCount() != 0 {
			t.Errorf("callCount() = %d, want 0 (should not call API if transcript too long)", server.callCount())
		}
	})

	t.Run("adds language instruction for non-English", func(t *testing.T) {
		t.Parallel()

		server := newMockDeepSeekServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, deepSeekResponse("Contenu restructuré."))

		r := mustNewDeepSeekRestructurer(t, "test-api-key",
			restructure.WithDeepSeekBaseURL(server.URL),
			restructure.WithDeepSeekRetryDelays(time.Millisecond, time.Millisecond),
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

		server := newMockDeepSeekServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, deepSeekResponse("Restructured content."))

		r := mustNewDeepSeekRestructurer(t, "test-api-key",
			restructure.WithDeepSeekBaseURL(server.URL),
			restructure.WithDeepSeekRetryDelays(time.Millisecond, time.Millisecond),
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

	t.Run("API returns empty choices", func(t *testing.T) {
		t.Parallel()

		server := newMockDeepSeekServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, map[string]any{
			"id":      "test",
			"choices": []any{}, // Empty
		})

		r := mustNewDeepSeekRestructurer(t, "test-api-key",
			restructure.WithDeepSeekBaseURL(server.URL),
			restructure.WithDeepSeekMaxRetries(0),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err == nil {
			t.Fatal("Restructure() with empty choices expected error, got nil")
		}

		if !strings.Contains(err.Error(), "no response") {
			t.Errorf("Restructure() error = %q, want containing %q", err.Error(), "no response")
		}
	})

	t.Run("uses deepseek-reasoner model by default", func(t *testing.T) {
		t.Parallel()

		server := newMockDeepSeekServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, deepSeekResponse("result"))

		r := mustNewDeepSeekRestructurer(t, "test-api-key",
			restructure.WithDeepSeekBaseURL(server.URL),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		call := server.lastCall()
		if call.Model != "deepseek-reasoner" {
			t.Errorf("lastCall().Model = %q, want %q", call.Model, "deepseek-reasoner")
		}
	})

	t.Run("custom model can be set", func(t *testing.T) {
		t.Parallel()

		server := newMockDeepSeekServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, deepSeekResponse("result"))

		r := mustNewDeepSeekRestructurer(t, "test-api-key",
			restructure.WithDeepSeekBaseURL(server.URL),
			restructure.WithDeepSeekModel("deepseek-chat"),
		)

		_, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		call := server.lastCall()
		if call.Model != "deepseek-chat" {
			t.Errorf("lastCall().Model = %q, want %q", call.Model, "deepseek-chat")
		}
	})
}

// ---------------------------------------------------------------------------
// TestDeepSeekRestructurer_HTTPErrors - API error handling
// ---------------------------------------------------------------------------

func TestDeepSeekRestructurer_HTTPErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       any
		wantErr    error
		retryable  bool
	}{
		{
			name:       "401 unauthorized",
			statusCode: http.StatusUnauthorized,
			body:       deepSeekErrorResponse("Invalid API key", "invalid_api_key", "401"),
			wantErr:    apierr.ErrAuthFailed,
			retryable:  false,
		},
		{
			name:       "402 payment required",
			statusCode: http.StatusPaymentRequired,
			body:       deepSeekErrorResponse("Insufficient balance", "insufficient_balance", "402"),
			wantErr:    apierr.ErrQuotaExceeded,
			retryable:  false,
		},
		{
			name:       "429 rate limit",
			statusCode: http.StatusTooManyRequests,
			body:       deepSeekErrorResponse("Rate limit exceeded", "rate_limit", "429"),
			wantErr:    apierr.ErrRateLimit,
			retryable:  true,
		},
		{
			name:       "500 server error",
			statusCode: http.StatusInternalServerError,
			body:       deepSeekErrorResponse("Internal server error", "server_error", "500"),
			wantErr:    nil, // Server errors are retryable but don't wrap a specific sentinel
			retryable:  true,
		},
		{
			name:       "503 service unavailable",
			statusCode: http.StatusServiceUnavailable,
			body:       deepSeekErrorResponse("Service overloaded", "server_error", "503"),
			wantErr:    nil,
			retryable:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := newMockDeepSeekServer()
			t.Cleanup(server.Close)

			// For retryable errors, add multiple failures then success
			if tt.retryable {
				server.addResponse(tt.statusCode, tt.body)
				server.addResponse(tt.statusCode, tt.body)
				server.addResponse(http.StatusOK, deepSeekResponse("success"))
			} else {
				server.addResponse(tt.statusCode, tt.body)
			}

			r := mustNewDeepSeekRestructurer(t, "test-api-key",
				restructure.WithDeepSeekBaseURL(server.URL),
				restructure.WithDeepSeekMaxRetries(2),
				restructure.WithDeepSeekRetryDelays(time.Millisecond, time.Millisecond),
			)

			result, err := r.Restructure(context.Background(), "transcript", template.MustParseName("meeting"), lang.Language{})

			if tt.retryable {
				// Should eventually succeed after retries
				if err != nil {
					t.Fatalf("Restructure() with retryable error, expected success after retries, got error: %v", err)
				}
				if result != "success" {
					t.Errorf("Restructure() = %q, want %q", result, "success")
				}
				if server.callCount() != 3 {
					t.Errorf("callCount() = %d, want 3 (2 failures + 1 success)", server.callCount())
				}
			} else {
				// Should fail without retry
				if err == nil {
					t.Fatal("Restructure() with non-retryable error expected error, got nil")
				}
				if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
					t.Errorf("Restructure() error = %v, want error wrapping %v", err, tt.wantErr)
				}
				if server.callCount() != 1 {
					t.Errorf("callCount() = %d, want 1 (no retry)", server.callCount())
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestDeepSeekRestructurer_WithMapReduce - MapReduce integration
// ---------------------------------------------------------------------------

func TestDeepSeekRestructurer_WithMapReduce(t *testing.T) {
	t.Parallel()

	t.Run("MapReduce works with DeepSeekRestructurer", func(t *testing.T) {
		t.Parallel()

		server := newMockDeepSeekServer()
		t.Cleanup(server.Close)

		// Add responses for: 2 map calls + 1 reduce call
		server.addResponse(http.StatusOK, deepSeekResponse("# Part 1 Result"))
		server.addResponse(http.StatusOK, deepSeekResponse("# Part 2 Result"))
		server.addResponse(http.StatusOK, deepSeekResponse("# Merged Final Result"))

		base := mustNewDeepSeekRestructurer(t, "test-api-key",
			restructure.WithDeepSeekBaseURL(server.URL),
			restructure.WithDeepSeekRetryDelays(time.Millisecond, time.Millisecond),
		)

		mr := restructure.NewMapReduceRestructurer(base,
			restructure.WithMapReduceMaxTokens(50), // Force splitting
		)

		// Create paragraphs that will split into 2 chunks
		para1 := strings.Repeat("a", 300) // ~100 tokens
		para2 := strings.Repeat("b", 300) // ~100 tokens
		transcript := para1 + "\n\n" + para2

		result, usedMapReduce, err := mr.Restructure(context.Background(), transcript, template.MustParseName("meeting"), lang.Language{})
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		if !usedMapReduce {
			t.Errorf("Restructure(long_transcript) usedMapReduce = false, want true")
		}

		if result != "# Merged Final Result" {
			t.Errorf("Restructure() = %q, want %q", result, "# Merged Final Result")
		}

		if server.callCount() != 3 {
			t.Errorf("callCount() = %d, want 3 (2 map + 1 reduce)", server.callCount())
		}
	})

	t.Run("short transcript skips MapReduce with DeepSeek", func(t *testing.T) {
		t.Parallel()

		server := newMockDeepSeekServer()
		t.Cleanup(server.Close)

		server.addResponse(http.StatusOK, deepSeekResponse("Simple result."))

		base := mustNewDeepSeekRestructurer(t, "test-api-key",
			restructure.WithDeepSeekBaseURL(server.URL),
			restructure.WithDeepSeekRetryDelays(time.Millisecond, time.Millisecond),
		)

		mr := restructure.NewMapReduceRestructurer(base,
			restructure.WithMapReduceMaxTokens(1000), // High limit
		)

		result, usedMapReduce, err := mr.Restructure(context.Background(), "Short transcript.", template.MustParseName("meeting"), lang.Language{})
		if err != nil {
			t.Fatalf("Restructure() unexpected error: %v", err)
		}

		if usedMapReduce {
			t.Errorf("Restructure(short_transcript) usedMapReduce = true, want false")
		}

		if result != "Simple result." {
			t.Errorf("Restructure() = %q, want %q", result, "Simple result.")
		}

		if server.callCount() != 1 {
			t.Errorf("callCount() = %d, want 1", server.callCount())
		}
	})
}
