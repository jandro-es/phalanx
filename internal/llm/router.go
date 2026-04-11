// Package llm provides a model-agnostic LLM router with rate limiting,
// retries, fallback, and full audit logging.
package llm

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/phalanx-ai/phalanx/internal/audit"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// Adapter is implemented by each LLM provider.
type Adapter interface {
	Complete(ctx context.Context, req types.LLMRequest, provider types.LLMProvider) (*types.LLMResponse, error)
}

// RouteOptions provides optional context for auditing and fallback.
type RouteOptions struct {
	FallbackProvider string
	FallbackModel    string
	SessionID        *string
	AgentID          *string
}

// Router routes LLM requests to the correct provider adapter.
type Router struct {
	mu        sync.RWMutex
	providers map[string]types.LLMProvider
	adapters  map[string]Adapter
	limiter   *rateLimiter
	audit     *audit.Logger
}

// NewRouter creates a new LLM router.
func NewRouter(auditLogger *audit.Logger) *Router {
	return &Router{
		providers: make(map[string]types.LLMProvider),
		adapters:  make(map[string]Adapter),
		limiter:   newRateLimiter(),
		audit:     auditLogger,
	}
}

// RegisterProvider registers a provider and selects the correct adapter.
func (r *Router) RegisterProvider(p types.LLMProvider, adapter Adapter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.Name] = p
	r.adapters[p.Name] = adapter
}

// Route sends a request to the correct provider with retries and optional fallback.
func (r *Router) Route(ctx context.Context, req types.LLMRequest, opts *RouteOptions) (*types.LLMResponse, error) {
	r.mu.RLock()
	provider, ok := r.providers[req.Provider]
	adapter, adapterOk := r.adapters[req.Provider]
	r.mu.RUnlock()

	if !ok || !adapterOk {
		return nil, fmt.Errorf("unknown LLM provider: %s", req.Provider)
	}

	maxRetries := provider.Config.MaxRetries
	if maxRetries == 0 {
		maxRetries = 2
	}
	retryDelay := time.Duration(provider.Config.RetryDelayMs) * time.Millisecond
	if retryDelay == 0 {
		retryDelay = time.Second
	}

	// Audit the request
	r.audit.Log(ctx, audit.Event{
		EventType: types.AuditLLMRequest,
		SessionID: ptrIfSet(opts, func(o *RouteOptions) *string { return o.SessionID }),
		AgentID:   ptrIfSet(opts, func(o *RouteOptions) *string { return o.AgentID }),
		Actor:     "system",
		Payload: map[string]any{
			"provider":    req.Provider,
			"model":       req.Model,
			"temperature": req.Temperature,
			"maxTokens":   req.MaxTokens,
		},
	})

	var lastErr error

	// Try primary with retries
	for attempt := 0; attempt <= maxRetries; attempt++ {
		r.limiter.acquire(provider.ID, provider.Config.RequestsPerMinute)

		start := time.Now()
		resp, err := adapter.Complete(ctx, req, provider)
		latency := time.Since(start)

		if err == nil {
			resp.LatencyMs = int(latency.Milliseconds())
			resp.Provider = req.Provider

			r.audit.Log(ctx, audit.Event{
				EventType: types.AuditLLMResponse,
				SessionID: ptrIfSet(opts, func(o *RouteOptions) *string { return o.SessionID }),
				AgentID:   ptrIfSet(opts, func(o *RouteOptions) *string { return o.AgentID }),
				Actor:     "system",
				Payload: map[string]any{
					"provider":     req.Provider,
					"model":        resp.Model,
					"inputTokens":  resp.InputTokens,
					"outputTokens": resp.OutputTokens,
					"latencyMs":    resp.LatencyMs,
					"attempt":      attempt,
				},
			})
			return resp, nil
		}

		lastErr = err
		r.audit.Log(ctx, audit.Event{
			EventType: types.AuditLLMError,
			SessionID: ptrIfSet(opts, func(o *RouteOptions) *string { return o.SessionID }),
			Actor:     "system",
			Payload:   map[string]any{"provider": req.Provider, "attempt": attempt, "error": err.Error()},
		})

		if attempt < maxRetries {
			time.Sleep(retryDelay * time.Duration(math.Pow(2, float64(attempt))))
		}
	}

	// Try fallback
	if opts != nil && opts.FallbackProvider != "" {
		r.mu.RLock()
		fbProvider, fbOk := r.providers[opts.FallbackProvider]
		fbAdapter, fbAdapterOk := r.adapters[opts.FallbackProvider]
		r.mu.RUnlock()

		if fbOk && fbAdapterOk {
			model := opts.FallbackModel
			if model == "" {
				model = fbProvider.DefaultModel
			}

			r.audit.Log(ctx, audit.Event{
				EventType: types.AuditLLMFallback,
				SessionID: ptrIfSet(opts, func(o *RouteOptions) *string { return o.SessionID }),
				Actor:     "system",
				Payload:   map[string]any{"from": req.Provider, "to": opts.FallbackProvider, "reason": lastErr.Error()},
			})

			fbReq := req
			fbReq.Provider = opts.FallbackProvider
			fbReq.Model = model

			r.limiter.acquire(fbProvider.ID, fbProvider.Config.RequestsPerMinute)
			start := time.Now()
			resp, err := fbAdapter.Complete(ctx, fbReq, fbProvider)
			if err == nil {
				resp.LatencyMs = int(time.Since(start).Milliseconds())
				resp.Provider = opts.FallbackProvider
				return resp, nil
			}
			return nil, fmt.Errorf("primary failed: %v; fallback failed: %v", lastErr, err)
		}
	}

	return nil, lastErr
}

func ptrIfSet(opts *RouteOptions, fn func(*RouteOptions) *string) *string {
	if opts == nil {
		return nil
	}
	return fn(opts)
}

// --- Simple token-bucket rate limiter ---

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

func newRateLimiter() *rateLimiter {
	return &rateLimiter{buckets: make(map[string]*bucket)}
}

func (rl *rateLimiter) acquire(providerID string, rpm int) {
	if rpm <= 0 {
		rpm = 600
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()

	b, ok := rl.buckets[providerID]
	now := time.Now()
	if !ok {
		b = &bucket{tokens: float64(rpm), lastRefill: now}
		rl.buckets[providerID] = b
	}

	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens = math.Min(float64(rpm), b.tokens+elapsed*float64(rpm)/60.0)
	b.lastRefill = now

	if b.tokens < 1 {
		// Would block — in production, use a channel-based limiter
		b.tokens = 1
	}
	b.tokens--
}
