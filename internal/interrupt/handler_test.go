package interrupt_test

// Notes:
// - Tests use black-box approach via interrupt_test package
// - All tests inject dependencies via NewHandlerWithOptions for deterministic behavior
// - Time manipulation: nowFunc is injected to control interruptWindow calculation
// - Signal synchronization: ctx.Done() used to confirm first signal processed
// - Coverage gaps: None intentional - target is 95%
//
// Test timing strategy:
// - For WaitForDecision tests, we manipulate nowFunc so that "elapsed" is close
//   to interruptWindow (2s), making "remaining" very small (~50ms) for fast tests.
//
// Thread-safety note:
// - Production code writes to stderr from both listen() and WaitForDecision()
// - os.Stderr is safe for concurrent writes at OS level
// - bytes.Buffer is NOT thread-safe, so we use syncBuffer in tests

import (
	"bytes"
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alnah/transcript/internal/interrupt"
)

// syncBuffer is a thread-safe bytes.Buffer for testing.
// Required because the Handler writes to stderr from multiple goroutines.
// NOTE: If other packages need a thread-safe buffer for stderr tests,
// consider extracting this to internal/testutil.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Bytes()
}

func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}

func (b *syncBuffer) Contains(substr string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.Contains(b.buf.Bytes(), []byte(substr))
}

// ---------------------------------------------------------------------------
// TestNewHandler - Default constructor
// ---------------------------------------------------------------------------

func TestNewHandler(t *testing.T) {
	t.Parallel()

	// NewHandler creates a real signal listener, so we just verify it returns
	// valid objects and can be stopped without panic.
	ctx := context.Background()
	h, handlerCtx := interrupt.NewHandler(ctx)

	// Handler and context should be non-nil
	if h == nil {
		t.Fatalf("NewHandler() handler = nil, want non-nil")
	}
	if handlerCtx == nil {
		t.Fatalf("NewHandler() context = nil, want non-nil")
	}

	// Context should not be canceled yet
	select {
	case <-handlerCtx.Done():
		t.Fatalf("NewHandler() context canceled before signal, want active")
	default:
		// Expected
	}

	// WasInterrupted should be false
	if got := h.WasInterrupted(); got {
		t.Errorf("NewHandler() WasInterrupted() = %v, want false", got)
	}

	// Stop should not panic
	h.Stop()
}

// ---------------------------------------------------------------------------
// TestHandler_FirstInterrupt - Single signal cancels context
// ---------------------------------------------------------------------------

func TestHandler_FirstInterrupt(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)
	var stderr syncBuffer

	h, ctx := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh:  sigCh,
		Stderr: &stderr,
	})
	defer h.Stop()

	// Send first signal
	sigCh <- os.Interrupt

	// Wait for context to be canceled (with timeout)
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("NewHandlerWithOptions() context not canceled after 100ms, want canceled")
	}

	// WasInterrupted should be true
	if got := h.WasInterrupted(); !got {
		t.Errorf("WasInterrupted() = %v, want true", got)
	}
}

// ---------------------------------------------------------------------------
// TestHandler_DoubleInterruptWithinWindow - Triggers abort
// ---------------------------------------------------------------------------

func TestHandler_DoubleInterruptWithinWindow(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)
	var stderr syncBuffer
	var exitCode atomic.Int32
	exitCode.Store(-1) // Sentinel: not called

	// Mock time: first signal at T=0, second at T=1s (within 2s window)
	callCount := 0
	var mu sync.Mutex
	mockNow := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if callCount == 1 {
			// First call: during first interrupt
			return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		// Subsequent calls: 1 second later (within window)
		return time.Date(2024, 1, 1, 0, 0, 1, 0, time.UTC)
	}

	h, ctx := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh:    sigCh,
		ExitFunc: func(code int) { exitCode.Store(int32(code)) },
		NowFunc:  mockNow,
		Stderr:   &stderr,
	})
	defer h.Stop()

	// Send first signal
	sigCh <- os.Interrupt

	// Wait for context cancellation (confirms first signal processed)
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("NewHandlerWithOptions() context not canceled after 100ms, want canceled")
	}

	// Send second signal (within window)
	sigCh <- os.Interrupt

	// Wait for exit to be called
	deadline := time.After(100 * time.Millisecond)
	for exitCode.Load() == -1 {
		select {
		case <-deadline:
			t.Fatalf("exitFunc not called after 100ms, want called")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	// Verify exit code
	if got := exitCode.Load(); got != 130 {
		t.Errorf("exitFunc(code) = %d, want 130", got)
	}

	// Verify stderr message
	if !stderr.Contains("Aborted.") {
		t.Errorf("stderr = %q, want containing %q", stderr.String(), "Aborted.")
	}
}

// ---------------------------------------------------------------------------
// TestHandler_DoubleInterruptOutsideWindow - Does NOT trigger abort
// ---------------------------------------------------------------------------

func TestHandler_DoubleInterruptOutsideWindow(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)
	var stderr syncBuffer
	var exitCalled atomic.Bool // Issue 1 fix: use atomic for thread-safety

	// Mock time: first signal at T=0, second at T=3s (outside 2s window)
	callCount := 0
	var mu sync.Mutex
	mockNow := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if callCount == 1 {
			return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		}
		// 3 seconds later - outside the 2s window
		return time.Date(2024, 1, 1, 0, 0, 3, 0, time.UTC)
	}

	h, ctx := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh:    sigCh,
		ExitFunc: func(code int) { exitCalled.Store(true) },
		NowFunc:  mockNow,
		Stderr:   &stderr,
	})
	defer h.Stop()

	// Send first signal
	sigCh <- os.Interrupt

	// Wait for context cancellation
	select {
	case <-ctx.Done():
		// Expected
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("NewHandlerWithOptions() context not canceled after 100ms, want canceled")
	}

	// Send second signal (outside window)
	sigCh <- os.Interrupt

	// Give time for processing
	time.Sleep(50 * time.Millisecond)

	// Exit should NOT have been called
	if got := exitCalled.Load(); got {
		t.Errorf("exitFunc called = %v, want false (second signal outside window)", got)
	}

	// Handler should still report interrupted
	if got := h.WasInterrupted(); !got {
		t.Errorf("WasInterrupted() = %v, want true", got)
	}
}

// ---------------------------------------------------------------------------
// TestHandler_WaitForDecision_Continue - Returns Continue after timeout
// ---------------------------------------------------------------------------

func TestHandler_WaitForDecision_Continue(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)
	var stderr syncBuffer

	// Mock time: first interrupt at T=0, then subsequent calls at T=1.95s
	// This makes remaining = 2s - 1.95s = 50ms (fast test)
	callCount := 0
	var mu sync.Mutex
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockNow := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if callCount == 1 {
			// First call: when first interrupt is recorded
			return baseTime
		}
		// Subsequent calls: 1.95s later, so remaining ~50ms
		return baseTime.Add(1950 * time.Millisecond)
	}

	h, ctx := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh:   sigCh,
		NowFunc: mockNow,
		Stderr:  &stderr,
	})
	defer h.Stop()

	// Send first signal
	sigCh <- os.Interrupt

	// Wait for context cancellation
	select {
	case <-ctx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("NewHandlerWithOptions() context not canceled after 100ms, want canceled")
	}

	// Call WaitForDecision - should return Continue after ~50ms
	start := time.Now()
	behavior := h.WaitForDecision("Press Ctrl+C again to abort...")
	elapsed := time.Since(start)

	if behavior != interrupt.Continue {
		t.Errorf("WaitForDecision() = %v, want Continue", behavior)
	}

	// Should have taken approximately 50ms (with generous margin)
	if elapsed > 500*time.Millisecond {
		t.Errorf("WaitForDecision() took %v, want ~50ms", elapsed)
	}

	// Message should have been written to stderr
	const wantMsg = "Press Ctrl+C again to abort..."
	if !stderr.Contains(wantMsg) {
		t.Errorf("stderr = %q, want containing %q", stderr.String(), wantMsg)
	}
}

// ---------------------------------------------------------------------------
// TestHandler_WaitForDecision_Abort - Returns Abort on second signal
// ---------------------------------------------------------------------------

func TestHandler_WaitForDecision_Abort(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)
	var stderr syncBuffer
	var exitCode atomic.Int32
	exitCode.Store(-1)

	// Mock time: all calls return same time (within window)
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockNow := func() time.Time {
		return baseTime
	}

	h, ctx := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh:    sigCh,
		ExitFunc: func(code int) { exitCode.Store(int32(code)) },
		NowFunc:  mockNow,
		Stderr:   &stderr,
	})
	defer h.Stop()

	// Send first signal
	sigCh <- os.Interrupt

	// Wait for context cancellation
	select {
	case <-ctx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("NewHandlerWithOptions() context not canceled after 100ms, want canceled")
	}

	// Start WaitForDecision in goroutine
	behaviorCh := make(chan interrupt.Behavior, 1)
	go func() {
		behaviorCh <- h.WaitForDecision("Press Ctrl+C again to abort...")
	}()

	// Small delay to ensure WaitForDecision is waiting
	time.Sleep(20 * time.Millisecond)

	// Send second signal (triggers abort via listen goroutine)
	sigCh <- os.Interrupt

	// Wait for WaitForDecision to return or exitFunc to be called
	select {
	case behavior := <-behaviorCh:
		if behavior != interrupt.Abort {
			t.Errorf("WaitForDecision() = %v, want Abort", behavior)
		}
	case <-time.After(500 * time.Millisecond):
		// exitFunc was called (which doesn't return in real code)
		// Check that it was called with correct code
		if exitCode.Load() != 130 {
			t.Fatalf("WaitForDecision() did not return and exitFunc not called with 130")
		}
	}
}

// ---------------------------------------------------------------------------
// TestHandler_WaitForDecision_NotInterrupted - Returns immediately
// ---------------------------------------------------------------------------

func TestHandler_WaitForDecision_NotInterrupted(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)
	var stderr syncBuffer

	h, _ := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh:  sigCh,
		Stderr: &stderr,
	})
	defer h.Stop()

	// No signal sent - WaitForDecision should return immediately
	start := time.Now()
	behavior := h.WaitForDecision("This message should not appear")
	elapsed := time.Since(start)

	if behavior != interrupt.Continue {
		t.Errorf("WaitForDecision() = %v, want Continue", behavior)
	}

	// Should return almost immediately (< 10ms)
	if elapsed > 50*time.Millisecond {
		t.Errorf("WaitForDecision() took %v, want immediate return", elapsed)
	}

	// Message should NOT have been written (fast path)
	if got := stderr.Len(); got > 0 {
		t.Errorf("stderr length = %d, want 0 (got: %q)", got, stderr.String())
	}
}

// ---------------------------------------------------------------------------
// TestHandler_WaitForDecision_AlreadyAborted - Returns Abort immediately
// ---------------------------------------------------------------------------

func TestHandler_WaitForDecision_AlreadyAborted(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)
	var stderr syncBuffer
	exitCalled := make(chan struct{})

	// Mock time: all within window
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockNow := func() time.Time { return baseTime }

	h, ctx := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh:    sigCh,
		ExitFunc: func(code int) { close(exitCalled) },
		NowFunc:  mockNow,
		Stderr:   &stderr,
	})
	defer h.Stop()

	// Send two signals quickly to trigger abort
	sigCh <- os.Interrupt

	select {
	case <-ctx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("NewHandlerWithOptions() context not canceled after 100ms, want canceled")
	}

	sigCh <- os.Interrupt

	// Wait for exitFunc to be called
	select {
	case <-exitCalled:
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("exitFunc not called after 100ms, want called")
	}

	// Now WaitForDecision should return Abort immediately
	start := time.Now()
	behavior := h.WaitForDecision("message")
	elapsed := time.Since(start)

	if behavior != interrupt.Abort {
		t.Errorf("WaitForDecision() = %v, want Abort", behavior)
	}

	if elapsed > 50*time.Millisecond {
		t.Errorf("WaitForDecision() took %v, want immediate return", elapsed)
	}
}

// ---------------------------------------------------------------------------
// TestHandler_WaitForDecision_WindowExpired - Returns Continue immediately
// ---------------------------------------------------------------------------

func TestHandler_WaitForDecision_WindowExpired(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)
	var stderr syncBuffer

	// Mock time: first interrupt at T=0, WaitForDecision call at T=3s (window expired)
	callCount := 0
	var mu sync.Mutex
	baseTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mockNow := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		callCount++
		if callCount == 1 {
			return baseTime
		}
		// 3 seconds later - window already expired
		return baseTime.Add(3 * time.Second)
	}

	h, ctx := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh:   sigCh,
		NowFunc: mockNow,
		Stderr:  &stderr,
	})
	defer h.Stop()

	// Send first signal
	sigCh <- os.Interrupt

	select {
	case <-ctx.Done():
	case <-time.After(100 * time.Millisecond):
		t.Fatalf("NewHandlerWithOptions() context not canceled after 100ms, want canceled")
	}

	// WaitForDecision with expired window should return immediately
	start := time.Now()
	behavior := h.WaitForDecision("message")
	elapsed := time.Since(start)

	if behavior != interrupt.Continue {
		t.Errorf("WaitForDecision() = %v, want Continue", behavior)
	}

	if elapsed > 50*time.Millisecond {
		t.Errorf("WaitForDecision() took %v, want immediate return", elapsed)
	}
}

// ---------------------------------------------------------------------------
// TestHandler_Stop - Prevents further signal processing
// ---------------------------------------------------------------------------

func TestHandler_Stop(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)

	h, _ := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh: sigCh,
	})

	// Stop the handler
	h.Stop()

	// Send signal after stop
	sigCh <- os.Interrupt

	// Give time for potential processing
	time.Sleep(50 * time.Millisecond)

	// WasInterrupted should be false (signal was ignored)
	if got := h.WasInterrupted(); got {
		t.Errorf("WasInterrupted() = %v, want false after Stop()", got)
	}

	// Stop again should not panic (idempotent)
	h.Stop()
}

// ---------------------------------------------------------------------------
// TestHandler_NilSigCh - No listener started
// ---------------------------------------------------------------------------

func TestHandler_NilSigCh(t *testing.T) {
	t.Parallel()

	h, ctx := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh: nil, // No signal channel
	})
	defer h.Stop()

	// Context should not be nil
	if ctx == nil {
		t.Fatalf("NewHandlerWithOptions(nil sigCh) context = nil, want non-nil")
	}

	// WasInterrupted should be false
	if got := h.WasInterrupted(); got {
		t.Errorf("WasInterrupted() = %v, want false with nil sigCh", got)
	}

	// WaitForDecision should return Continue immediately
	behavior := h.WaitForDecision("message")
	if behavior != interrupt.Continue {
		t.Errorf("WaitForDecision() = %v, want Continue", behavior)
	}

	// Stop should not panic
	h.Stop()
}

// ---------------------------------------------------------------------------
// TestHandler_ChannelClosed - Listener exits gracefully
// ---------------------------------------------------------------------------

func TestHandler_ChannelClosed(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)

	h, _ := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh: sigCh,
	})
	defer h.Stop()

	// Close the channel (simulates cleanup)
	close(sigCh)

	// Give time for listener to notice
	time.Sleep(50 * time.Millisecond)

	// Should not panic, WasInterrupted still false
	if got := h.WasInterrupted(); got {
		t.Errorf("WasInterrupted() = %v, want false when channel closed without signal", got)
	}
}

// ---------------------------------------------------------------------------
// TestHandler_ParentContextCanceled - Handler respects parent
// ---------------------------------------------------------------------------

func TestHandler_ParentContextCanceled(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 2)
	parentCtx, parentCancel := context.WithCancel(context.Background())

	h, ctx := interrupt.NewHandlerWithOptions(parentCtx, interrupt.Options{
		SigCh: sigCh,
	})
	defer h.Stop()

	// Cancel parent context
	parentCancel()

	// Handler's context should also be canceled
	select {
	case <-ctx.Done():
		// Expected - parent cancellation propagates
	case <-time.After(100 * time.Millisecond):
		t.Errorf("NewHandlerWithOptions() context not canceled after parent cancel, want canceled")
	}

	// WasInterrupted should still be false (canceled by parent, not signal)
	if got := h.WasInterrupted(); got {
		t.Errorf("WasInterrupted() = %v, want false when canceled by parent", got)
	}
}

// ---------------------------------------------------------------------------
// TestConstants - Verify exported constants
// ---------------------------------------------------------------------------

func TestConstants(t *testing.T) {
	t.Parallel()

	// ExitInterrupt should be 130 (128 + SIGINT) - this is a Unix convention
	if interrupt.ExitInterrupt != 130 {
		t.Errorf("ExitInterrupt = %d, want 130 (Unix convention: 128 + SIGINT)", interrupt.ExitInterrupt)
	}

	// Behavior values must be distinct (exact values are implementation detail)
	if interrupt.Continue == interrupt.Abort {
		t.Error("Continue and Abort must be distinct values")
	}
}

// ---------------------------------------------------------------------------
// TestBehavior_String - Verify Behavior.String() method
// ---------------------------------------------------------------------------

func TestBehavior_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		behavior interrupt.Behavior
		want     string
	}{
		{interrupt.Continue, "Continue"},
		{interrupt.Abort, "Abort"},
		{interrupt.Behavior(99), "Behavior(99)"}, // Unknown value
	}

	for _, tt := range tests {
		if got := tt.behavior.String(); got != tt.want {
			t.Errorf("Behavior(%v).String() = %q, want %q", int(tt.behavior), got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// TestHandler_ConcurrentAccess - Thread safety
// ---------------------------------------------------------------------------

func TestHandler_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	sigCh := make(chan os.Signal, 10)
	var stderr syncBuffer

	h, _ := interrupt.NewHandlerWithOptions(context.Background(), interrupt.Options{
		SigCh:    sigCh,
		ExitFunc: func(code int) {}, // Don't exit
		Stderr:   &stderr,
	})
	defer h.Stop()

	var wg sync.WaitGroup
	const goroutines = 10

	// Multiple goroutines calling WasInterrupted
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = h.WasInterrupted()
			}
		}()
	}

	// Send some signals while goroutines are running
	for i := 0; i < 3; i++ {
		sigCh <- os.Interrupt
		time.Sleep(10 * time.Millisecond)
	}

	wg.Wait()

	// If we get here without race detector complaints, we're good
}
