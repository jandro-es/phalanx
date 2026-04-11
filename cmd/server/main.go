// Phalanx server — main entry point.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/phalanx-ai/phalanx/internal/api"
	"github.com/phalanx-ai/phalanx/internal/audit"
	"github.com/phalanx-ai/phalanx/internal/config"
	"github.com/phalanx-ai/phalanx/internal/llm"
	"github.com/phalanx-ai/phalanx/internal/llm/adapters"
	"github.com/phalanx-ai/phalanx/internal/orchestrator"
	"github.com/phalanx-ai/phalanx/internal/platform"
	"github.com/phalanx-ai/phalanx/internal/queue"
	"github.com/phalanx-ai/phalanx/internal/report"
	"github.com/phalanx-ai/phalanx/internal/types"
)

func main() {
	cfg := config.Load()

	// Logger
	level, _ := zerolog.ParseLevel(cfg.LogLevel)
	zerolog.SetGlobalLevel(level)
	if os.Getenv("NODE_ENV") != "production" {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	// Database
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to connect to database")
	}
	defer pool.Close()

	if err := pool.Ping(ctx); err != nil {
		log.Fatal().Err(err).Msg("Database ping failed")
	}
	log.Info().Msg("Connected to PostgreSQL")

	// Audit logger
	auditLogger := audit.New(pool, cfg.AuditHashChain)

	// LLM Router
	router := llm.NewRouter(auditLogger)
	anthropicAdapter := adapters.NewAnthropicAdapter()
	openaiAdapter := adapters.NewOpenAICompatAdapter()

	// Register providers from database
	if err := loadProviders(ctx, pool, router, anthropicAdapter, openaiAdapter); err != nil {
		log.Warn().Err(err).Msg("Failed to load some LLM providers")
	}

	// Git platform clients
	platforms := map[types.Platform]platform.Client{}
	if cfg.GitHubToken != "" {
		platforms[types.PlatformGitHub] = platform.NewGitHubClient(cfg.GitHubToken, cfg.GitHubAPIURL)
		log.Info().Msg("GitHub integration enabled")
	}
	if cfg.GitLabToken != "" {
		platforms[types.PlatformGitLab] = platform.NewGitLabClient(cfg.GitLabToken, cfg.GitLabURL)
		log.Info().Msg("GitLab integration enabled")
	}

	// Orchestrator (acts as the async review worker)
	builder := &report.Builder{}
	orch := orchestrator.New(pool, auditLogger, router, builder, platforms, cfg.QueueConcurrency)

	// Queue client (enqueue) + server (worker). The worker dispatches
	// dequeued tasks back into the orchestrator.
	queueOpts := queue.Options{
		RedisURL:     cfg.RedisURL,
		Concurrency:  cfg.QueueConcurrency,
		MaxRetries:   cfg.QueueMaxRetries,
		JobTimeoutMs: cfg.QueueJobTimeout,
	}
	queueClient, err := queue.NewClient(queueOpts, auditLogger)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to build queue client")
	}
	defer queueClient.Close()

	queueServer, err := queue.NewServer(queueOpts,
		func(taskCtx context.Context, session types.ReviewSession) error {
			_, err := orch.ExecuteReview(taskCtx, session)
			return err
		},
		func(loadCtx context.Context, id string) (*types.ReviewSession, error) {
			return loadSessionByID(loadCtx, pool, id)
		},
	)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to build queue server")
	}
	if err := queueServer.Start(); err != nil {
		log.Fatal().Err(err).Msg("Queue worker failed to start")
	}
	defer queueServer.Shutdown()
	log.Info().Int("concurrency", cfg.QueueConcurrency).Msg("Review queue worker started")

	// HTTP Server
	handler := &api.Handler{DB: pool, Audit: auditLogger, Enqueuer: queueClient}

	r := chi.NewRouter()
	r.Use(chimw.Logger)
	r.Use(chimw.Recoverer)
	r.Use(chimw.RealIP)
	r.Use(chimw.RequestID)
	r.Use(chimw.Timeout(30 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: []string{"*"},
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders: []string{"*"},
	}))

	r.Mount("/", handler.Routes())

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	srv := &http.Server{Addr: addr, Handler: r}

	// Graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
		<-sigCh
		log.Info().Msg("Shutting down...")
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	log.Info().Str("addr", addr).Msg("Phalanx server starting")
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("Server failed")
	}
}

// loadProviders reads all llm_providers rows and registers them with the router,
// choosing the Anthropic adapter for Anthropic-compatible providers and the
// OpenAI adapter for everything else.
func loadProviders(
	ctx context.Context,
	pool *pgxpool.Pool,
	router *llm.Router,
	anthropicAdapter llm.Adapter,
	openaiAdapter llm.Adapter,
) error {
	rows, err := pool.Query(ctx,
		`SELECT id, name, base_url, auth_method, api_key_ref, default_model,
		        models, config, created_at, updated_at
		 FROM llm_providers`)
	if err != nil {
		return fmt.Errorf("query providers: %w", err)
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var p types.LLMProvider
		var configRaw []byte

		if err := rows.Scan(
			&p.ID, &p.Name, &p.BaseURL, &p.AuthMethod, &p.APIKeyRef,
			&p.DefaultModel, &p.Models, &configRaw, &p.CreatedAt, &p.UpdatedAt,
		); err != nil {
			log.Warn().Err(err).Msg("Skipping provider row (scan failed)")
			continue
		}
		if len(configRaw) > 0 {
			if err := json.Unmarshal(configRaw, &p.Config); err != nil {
				log.Warn().Err(err).Str("provider", p.Name).Msg("Invalid provider config JSON")
			}
		}

		var adapter llm.Adapter = openaiAdapter
		if strings.Contains(p.Name, "anthropic") || strings.Contains(p.BaseURL, "anthropic") {
			adapter = anthropicAdapter
		}
		router.RegisterProvider(p, adapter)
		log.Info().Str("provider", p.Name).Str("model", p.DefaultModel).Msg("Registered LLM provider")
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate providers: %w", err)
	}
	log.Info().Int("count", count).Msg("LLM providers loaded")
	return nil
}

// loadSessionByID hydrates a review session from Postgres for the queue worker.
func loadSessionByID(ctx context.Context, pool *pgxpool.Pool, id string) (*types.ReviewSession, error) {
	row := pool.QueryRow(ctx,
		`SELECT id, external_pr_id, platform, repository_full_name, pr_number,
		        pr_title, pr_author, pr_url, head_sha, base_sha, base_branch, head_branch,
		        diff_snapshot, file_tree, status, composite_report, overall_verdict,
		        trigger_source, metadata, started_at, completed_at
		 FROM review_sessions WHERE id = $1`, id)

	var s types.ReviewSession
	if err := row.Scan(
		&s.ID, &s.ExternalPRID, &s.Platform, &s.RepositoryFullName, &s.PRNumber,
		&s.PRTitle, &s.PRAuthor, &s.PRURL, &s.HeadSHA, &s.BaseSHA,
		&s.BaseBranch, &s.HeadBranch, &s.DiffSnapshot, &s.FileTree, &s.Status,
		&s.CompositeReport, &s.OverallVerdict, &s.TriggerSource, &s.Metadata,
		&s.StartedAt, &s.CompletedAt,
	); err != nil {
		return nil, err
	}
	return &s, nil
}
