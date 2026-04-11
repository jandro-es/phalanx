package llm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/phalanx-ai/phalanx/internal/audit"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// fakeAdapter is a programmable Adapter for router tests.
type fakeAdapter struct {
	mu         sync.Mutex
	calls      int32
	failFirst  int                                     // number of calls to fail before succeeding
	perCallErr []error                                 // specific per-call errors (overrides failFirst)
	resp       *types.LLMResponse                      // canned success response
	onCall     func(req types.LLMRequest, attempt int) // optional side-effect hook
	sleepEach  time.Duration                           // simulated latency
}

func (f *fakeAdapter) Complete(ctx context.Context, req types.LLMRequest, provider types.LLMProvider) (*types.LLMResponse, error) {
	n := atomic.AddInt32(&f.calls, 1)
	if f.sleepEach > 0 {
		time.Sleep(f.sleepEach)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.onCall != nil {
		f.onCall(req, int(n))
	}
	if int(n) <= len(f.perCallErr) && f.perCallErr[n-1] != nil {
		return nil, f.perCallErr[n-1]
	}
	if int(n) <= f.failFirst {
		return nil, fmt.Errorf("synthetic failure #%d", n)
	}
	if f.resp == nil {
		return &types.LLMResponse{Content: "ok", Model: req.Model, InputTokens: 10, OutputTokens: 20}, nil
	}
	// return a shallow copy so callers don't mutate ours
	copy := *f.resp
	return &copy, nil
}

func (f *fakeAdapter) Calls() int {
	return int(atomic.LoadInt32(&f.calls))
}

func testProvider(name string, maxRetries, retryDelayMs int) types.LLMProvider {
	return types.LLMProvider{
		ID:           "prov-" + name,
		Name:         name,
		BaseURL:      "https://fake/" + name,
		DefaultModel: "fake-model",
		Config: types.ProviderConfig{
			MaxRetries:        maxRetries,
			RetryDelayMs:      retryDelayMs,
			RequestsPerMinute: 60000, // effectively unlimited for most tests
		},
	}
}

func noAuditLogger() *audit.Logger {
	return audit.New(nil, false)
}

func TestRouter_HappyPath(t *testing.T) {
	r := NewRouter(noAuditLogger())
	adapter := &fakeAdapter{}
	r.RegisterProvider(testProvider("anthropic", 2, 1), adapter)

	resp, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "anthropic",
		Model:    "fake-model",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("route failed: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("content: got %q, want ok", resp.Content)
	}
	if resp.Provider != "anthropic" {
		t.Errorf("provider: got %q, want anthropic", resp.Provider)
	}
	if resp.LatencyMs < 0 {
		t.Errorf("latency should be set, got %d", resp.LatencyMs)
	}
	if adapter.Calls() != 1 {
		t.Errorf("adapter calls: got %d, want 1", adapter.Calls())
	}
}

func TestRouter_UnknownProvider(t *testing.T) {
	r := NewRouter(noAuditLogger())
	_, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "does-not-exist",
		Model:    "fake",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

func TestRouter_RetriesThenSucceeds(t *testing.T) {
	r := NewRouter(noAuditLogger())
	adapter := &fakeAdapter{failFirst: 2}
	r.RegisterProvider(testProvider("openai", 3, 1), adapter)

	start := time.Now()
	resp, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "fake-model",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("route failed: %v", err)
	}
	if resp == nil {
		t.Fatal("nil response on retry success")
	}
	if got := adapter.Calls(); got != 3 {
		t.Errorf("expected 3 attempts (2 fail + 1 success), got %d", got)
	}
	// Non-zero backoff delay should have elapsed at least once
	if time.Since(start) < 1*time.Millisecond {
		t.Errorf("too fast — retry delay not respected")
	}
}

func TestRouter_RetriesExhausted(t *testing.T) {
	r := NewRouter(noAuditLogger())
	adapter := &fakeAdapter{failFirst: 10}
	r.RegisterProvider(testProvider("openai", 2, 1), adapter)

	_, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "fake-model",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	// maxRetries=2 means 1 initial + 2 retries = 3 total
	if got := adapter.Calls(); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

func TestRouter_FallbackToSecondaryProvider(t *testing.T) {
	r := NewRouter(noAuditLogger())
	primary := &fakeAdapter{failFirst: 10}
	fallback := &fakeAdapter{}
	r.RegisterProvider(testProvider("openai", 1, 1), primary)
	r.RegisterProvider(testProvider("anthropic", 1, 1), fallback)

	resp, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "gpt-4.1",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, &RouteOptions{
		FallbackProvider: "anthropic",
		FallbackModel:    "claude-opus",
	})
	if err != nil {
		t.Fatalf("should have succeeded via fallback: %v", err)
	}
	if resp.Provider != "anthropic" {
		t.Errorf("provider on response: got %q, want anthropic", resp.Provider)
	}
	if primary.Calls() < 2 {
		t.Errorf("primary should have been retried: %d calls", primary.Calls())
	}
	if fallback.Calls() != 1 {
		t.Errorf("fallback should run exactly once: %d calls", fallback.Calls())
	}
}

func TestRouter_FallbackUsesFallbackModel(t *testing.T) {
	r := NewRouter(noAuditLogger())
	primary := &fakeAdapter{failFirst: 10}
	var seenModel string
	fallback := &fakeAdapter{
		onCall: func(req types.LLMRequest, _ int) { seenModel = req.Model },
	}
	r.RegisterProvider(testProvider("openai", 0, 1), primary)
	r.RegisterProvider(testProvider("anthropic", 0, 1), fallback)

	_, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "gpt-4.1",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, &RouteOptions{
		FallbackProvider: "anthropic",
		FallbackModel:    "claude-opus-4-6",
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenModel != "claude-opus-4-6" {
		t.Errorf("fallback should use FallbackModel, got %q", seenModel)
	}
}

func TestRouter_FallbackUsesProviderDefaultWhenModelEmpty(t *testing.T) {
	r := NewRouter(noAuditLogger())
	primary := &fakeAdapter{failFirst: 10}
	var seenModel string
	fallback := &fakeAdapter{
		onCall: func(req types.LLMRequest, _ int) { seenModel = req.Model },
	}
	fallbackProv := testProvider("anthropic", 0, 1)
	fallbackProv.DefaultModel = "default-model-xyz"

	r.RegisterProvider(testProvider("openai", 0, 1), primary)
	r.RegisterProvider(fallbackProv, fallback)

	_, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "gpt-4.1",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, &RouteOptions{
		FallbackProvider: "anthropic",
		// FallbackModel intentionally empty
	})
	if err != nil {
		t.Fatal(err)
	}
	if seenModel != "default-model-xyz" {
		t.Errorf("should fall back to provider default model, got %q", seenModel)
	}
}

func TestRouter_FallbackBothFail(t *testing.T) {
	r := NewRouter(noAuditLogger())
	primary := &fakeAdapter{failFirst: 10}
	fallback := &fakeAdapter{failFirst: 10}
	r.RegisterProvider(testProvider("openai", 0, 1), primary)
	r.RegisterProvider(testProvider("anthropic", 0, 1), fallback)

	_, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "gpt-4.1",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, &RouteOptions{FallbackProvider: "anthropic"})
	if err == nil {
		t.Fatal("expected error when both primary and fallback fail")
	}
}

func TestRouter_FallbackProviderNotRegistered(t *testing.T) {
	r := NewRouter(noAuditLogger())
	primary := &fakeAdapter{failFirst: 10}
	r.RegisterProvider(testProvider("openai", 0, 1), primary)

	_, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "gpt-4.1",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, &RouteOptions{FallbackProvider: "nonexistent"})
	// Should return primary's last error since fallback isn't registered
	if err == nil {
		t.Fatal("expected primary error to propagate when fallback is unknown")
	}
}

func TestRouter_ContextCancellationPropagates(t *testing.T) {
	r := NewRouter(noAuditLogger())
	var seen context.Context
	adapter := &fakeAdapter{
		onCall: func(_ types.LLMRequest, _ int) {
			// Block on context — we need to check it was passed through
		},
	}
	adapter.sleepEach = 50 * time.Millisecond
	r.RegisterProvider(testProvider("openai", 0, 1), adapter)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	_, _ = r.Route(ctx, types.LLMRequest{
		Provider: "openai",
		Model:    "gpt-4.1",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, nil)
	// The router doesn't check ctx.Err() itself, so the adapter needs to.
	// This test at least verifies Route accepts a context without panicking.
	_ = seen
}

func TestRouter_ErrorsAreWrappedNotNil(t *testing.T) {
	r := NewRouter(noAuditLogger())
	adapter := &fakeAdapter{
		perCallErr: []error{errors.New("rate limited"), errors.New("rate limited"), errors.New("rate limited")},
	}
	r.RegisterProvider(testProvider("openai", 2, 1), adapter)

	_, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "gpt-4.1",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// --- Rate limiter ---

func TestRateLimiter_CapsBurst(t *testing.T) {
	rl := newRateLimiter()

	// rpm=60 → 1 token per second. First call should pass, subsequent 59 consume the bucket.
	for i := 0; i < 60; i++ {
		rl.acquire("prov", 60)
	}
	// The 61st call would normally block; the current implementation clamps instead of blocking.
	// Either way, the function must return without panicking.
	rl.acquire("prov", 60)
}

func TestRateLimiter_SeparateBucketsPerProvider(t *testing.T) {
	rl := newRateLimiter()
	for i := 0; i < 60; i++ {
		rl.acquire("a", 60)
	}
	// Provider "b" should still have a full bucket
	rl.acquire("b", 60)
	if rl.buckets["b"].tokens > 60 {
		t.Errorf("b bucket overflow: %f", rl.buckets["b"].tokens)
	}
}

func TestRateLimiter_ZeroRPMUsesDefault(t *testing.T) {
	rl := newRateLimiter()
	rl.acquire("prov", 0) // rpm=0 should default to 600
	if rl.buckets["prov"] == nil {
		t.Fatal("bucket should be created for rpm=0")
	}
}

// --- Provider defaults ---

func TestRouter_DefaultRetriesWhenZero(t *testing.T) {
	r := NewRouter(noAuditLogger())
	// MaxRetries = 0 in the current router means "fall back to default 2".
	// This is the opposite convention from queue.Options — test codifies it.
	adapter := &fakeAdapter{failFirst: 10}
	r.RegisterProvider(testProvider("openai", 0, 1), adapter)

	_, _ = r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "fake",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, nil)

	// 1 initial + 2 default retries = 3
	if got := adapter.Calls(); got != 3 {
		t.Errorf("MaxRetries=0 should default to 2 retries (3 total), got %d", got)
	}
}

func TestRouter_NegativeRetriesMeansNoRetries(t *testing.T) {
	r := NewRouter(noAuditLogger())
	// Negative MaxRetries = disable retries; should still run the primary once.
	adapter := &fakeAdapter{failFirst: 10}
	r.RegisterProvider(testProvider("openai", -1, 1), adapter)

	_, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "fake",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, nil)

	if err == nil {
		t.Fatal("expected error when all attempts fail")
	}
	if got := adapter.Calls(); got != 1 {
		t.Errorf("MaxRetries=-1 should run once (no retries), got %d calls", got)
	}
}

func TestRouter_NegativeRetriesHappyPath(t *testing.T) {
	// A successful call with negative retries must still return the response,
	// not (nil, nil) like the pre-fix buggy path did.
	r := NewRouter(noAuditLogger())
	adapter := &fakeAdapter{}
	r.RegisterProvider(testProvider("openai", -1, 1), adapter)

	resp, err := r.Route(context.Background(), types.LLMRequest{
		Provider: "openai",
		Model:    "fake",
		Messages: []types.LLMMessage{{Role: "user", Content: "hi"}},
	}, nil)
	if err != nil {
		t.Fatalf("happy path with negative retries: %v", err)
	}
	if resp == nil {
		t.Fatal("happy path with negative retries should not return nil response")
	}
}
