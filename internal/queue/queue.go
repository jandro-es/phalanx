// Package queue provides a Redis-backed asynchronous review queue built on
// asynq. The API server enqueues `review:run` tasks via Client; an asynq.Server
// processes them with the orchestrator as the task handler. Concurrency,
// retries, and per-job timeout are driven by the Phalanx config.
package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/phalanx-ai/phalanx/internal/audit"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// TypeReview is the asynq task type for a full PR review run.
const TypeReview = "review:run"

// ReviewHandler is the function the worker invokes per dequeued review task.
// Returning an error lets asynq retry the task according to its policy.
type ReviewHandler func(ctx context.Context, session types.ReviewSession) error

// Options configures the queue client and server.
//
// MaxRetries=0 is a legitimate value (retries disabled). Use a negative value
// to request the default (2 retries).
type Options struct {
	RedisURL       string
	Concurrency    int
	MaxRetries     int
	JobTimeoutMs   int
	RetryDelayMs   int
	ShutdownTimout time.Duration
}

// Client enqueues review tasks. Callers use EnqueueReview.
type Client struct {
	client       *asynq.Client
	maxRetries   int
	jobTimeoutMs int
	audit        *audit.Logger
}

// NewClient builds a Client from the supplied Redis URL.
func NewClient(opts Options, auditLogger *audit.Logger) (*Client, error) {
	redisOpt, err := asynq.ParseRedisURI(opts.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	c := asynq.NewClient(redisOpt)

	maxRetries := opts.MaxRetries
	if maxRetries < 0 {
		maxRetries = 2
	}
	jobTimeout := opts.JobTimeoutMs
	if jobTimeout <= 0 {
		jobTimeout = 120000
	}

	return &Client{
		client:       c,
		maxRetries:   maxRetries,
		jobTimeoutMs: jobTimeout,
		audit:        auditLogger,
	}, nil
}

// Close releases the Redis connection.
func (c *Client) Close() error {
	return c.client.Close()
}

// EnqueueReview submits a review session to the queue. The session must
// already have been persisted — only the ID is carried in the task payload.
func (c *Client) EnqueueReview(ctx context.Context, session types.ReviewSession) error {
	payload, err := json.Marshal(reviewTaskPayload{SessionID: session.ID})
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	task := asynq.NewTask(TypeReview, payload,
		asynq.MaxRetry(c.maxRetries),
		asynq.Timeout(time.Duration(c.jobTimeoutMs)*time.Millisecond),
		asynq.Retention(24*time.Hour),
	)

	info, err := c.client.EnqueueContext(ctx, task)
	if err != nil {
		return fmt.Errorf("enqueue review: %w", err)
	}

	if c.audit != nil {
		sid := session.ID
		c.audit.Log(ctx, audit.Event{
			EventType: types.AuditSessionQueued,
			SessionID: &sid,
			Actor:     "api",
			Payload: map[string]any{
				"taskId":     info.ID,
				"queue":      info.Queue,
				"maxRetries": c.maxRetries,
				"timeoutMs":  c.jobTimeoutMs,
			},
		})
	}

	return nil
}

// Server wraps asynq.Server and dispatches review tasks to a ReviewHandler.
type Server struct {
	srv     *asynq.Server
	mux     *asynq.ServeMux
	handler ReviewHandler
	loader  sessionLoader
}

// sessionLoader resolves a persisted session by ID. This keeps the queue
// package free of any direct database dependency.
type sessionLoader func(ctx context.Context, id string) (*types.ReviewSession, error)

// NewServer constructs the worker server. `load` is called to hydrate the
// session from storage before invoking the handler so task payloads stay small.
func NewServer(opts Options, handler ReviewHandler, load sessionLoader) (*Server, error) {
	if handler == nil {
		return nil, fmt.Errorf("queue: handler is required")
	}
	if load == nil {
		return nil, fmt.Errorf("queue: session loader is required")
	}

	redisOpt, err := asynq.ParseRedisURI(opts.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}

	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 10
	}

	retryDelay := opts.RetryDelayMs
	if retryDelay <= 0 {
		retryDelay = 1000
	}

	cfg := asynq.Config{
		Concurrency: concurrency,
		RetryDelayFunc: func(n int, _ error, _ *asynq.Task) time.Duration {
			// exponential backoff, capped at 30s
			d := time.Duration(retryDelay) * time.Millisecond * (1 << n)
			if d > 30*time.Second {
				d = 30 * time.Second
			}
			return d
		},
		Queues: map[string]int{"default": 1},
	}

	s := &Server{
		srv:     asynq.NewServer(redisOpt, cfg),
		mux:     asynq.NewServeMux(),
		handler: handler,
		loader:  load,
	}
	s.mux.HandleFunc(TypeReview, s.handleReview)
	return s, nil
}

// Start launches the worker in a background goroutine.
func (s *Server) Start() error {
	return s.srv.Start(s.mux)
}

// Shutdown stops processing and waits for in-flight tasks to finish.
func (s *Server) Shutdown() {
	s.srv.Shutdown()
}

func (s *Server) handleReview(ctx context.Context, task *asynq.Task) error {
	var payload reviewTaskPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("%w: %v", asynq.SkipRetry, err)
	}

	session, err := s.loader(ctx, payload.SessionID)
	if err != nil {
		return fmt.Errorf("load session %s: %w", payload.SessionID, err)
	}

	return s.handler(ctx, *session)
}

type reviewTaskPayload struct {
	SessionID string `json:"sessionId"`
}
