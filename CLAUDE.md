# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Phalanx is an AI-powered multi-agent pull request review platform written in Go. It runs one specialised LLM agent per quality dimension (security, accessibility, performance, etc.) against a PR diff in parallel and posts a composite Markdown report back to GitHub/GitLab. Every LLM call and decision is recorded in an append-only audit log with optional SHA-256 hash chaining.

## Commands

```bash
make build          # builds bin/phalanx-server and bin/phalanx (CLI)
make build-linux    # static linux/amd64 binaries with -ldflags=-s -w
make run            # go run ./cmd/server (alias: make dev)
make test           # go test ./... -v -race -count=1
make test-cover     # writes coverage.out + coverage.html
make lint           # golangci-lint run ./...
make vet            # go vet ./...
make migrate        # psql $DATABASE_URL -f migrations/001_initial.sql
make seed           # registers every skills/*.yaml against a running server
make docker-up      # docker compose -f deploy/docker-compose.yml up -d
make helm-package   # mirror migrations into the chart and `helm package`
make helm-lint      # `helm lint` on the chart
```

Run a single test: `go test ./internal/agent -run TestParseResponse -v -race`.

The seed target requires `bin/phalanx` to exist and a server reachable at `http://localhost:3100`, so the typical first-run order is `make build && make migrate && make run` (in another shell) `&& make seed`.

The dashboard (`dashboard/`) is a separate React 19 + Vite 6 + Tailwind 4 build: `cd dashboard && npm run dev|build|preview`.

## Architecture

The server is a single Go binary that ties together five layers. Understanding how a webhook becomes a posted PR comment requires reading more than one file:

1. **`cmd/server/main.go`** boots the process: loads config (`internal/config`), opens a `pgxpool`, constructs the audit logger, builds the LLM router, scans `llm_providers` rows from Postgres and registers each one with either the Anthropic or OpenAI-compatible adapter (provider name/base_url is the only thing that picks the adapter — see `main.go:73`), wires up GitHub/GitLab platform clients if their tokens are set, and mounts `internal/api.Handler.Routes()` on a chi router.

2. **`internal/api/handlers.go`** is the HTTP surface: webhooks, `/api/reviews`, `/api/agents|skills|providers`, `/api/audit/*`, `/health`. Webhook handlers create a `review_sessions` row and hand the session to the orchestrator.

3. **`internal/queue/queue.go`** is the Redis-backed asynq layer. The server starts both an `asynq.Client` (enqueue side, exposed through `ReviewEnqueuer`) and an `asynq.Server` (worker side) in the same process. When a webhook or `/api/reviews` hit arrives, the handler persists a `review_sessions` row synchronously and enqueues a `review:run` task carrying only the session ID. The worker loads the session from Postgres (`loadSessionByID` in `cmd/server/main.go`) and invokes the orchestrator as its task handler. `PHALANX_QUEUE_CONCURRENCY`, `PHALANX_QUEUE_MAX_RETRIES`, and `PHALANX_QUEUE_JOB_TIMEOUT` all flow through. MaxRetries=0 means "no retries" — only a *negative* value in `queue.Options` requests the default.

4. **`internal/orchestrator/orchestrator.go`** runs `ExecuteReview` as the worker's task handler. It (a) fetches the diff via the platform client if not already snapshotted, (b) `loadAgents` joins `agents → skills → llm_providers` and eager-loads `context_documents` per agent, (c) fans out one goroutine per enabled agent bounded by `maxPar` (default 10), (d) persists each `agent_reports` row, (e) calls `report.Builder.BuildComposite` to roll up an overall verdict, and (f) posts back to the git platform. Every step also writes audit events. Returning a non-nil error from `ExecuteReview` tells asynq to retry the full review.

5. **`internal/agent/runtime.go`** is the per-agent execution unit. It optionally short-circuits via `agent.Config.FilePatterns` glob matching (the `matchGlob` helper supports `*`, `?`, `**`, and `**/`), assembles the system prompt from `skill.SystemPrompt + skill.ChecklistTemplate + agent.Contexts`, sends user message + diff through `llm.Router.Route`, then `parseResponse` extracts the verdict and checklist using the regexes `verdictRe` and `checklistRe` (look here when changing the markdown contract that skills must follow). Cost estimation uses a hardcoded `costTable` keyed by model name.

6. **`internal/llm/router.go`** is provider-agnostic. Adapters implement `Adapter.Complete`; built-in adapters live in `internal/llm/adapters/{anthropic.go, openai_compat.go}` (the OpenAI-compatible adapter handles OpenAI, DeepSeek, vLLM, Ollama, and anything else that speaks the OpenAI Chat Completions shape). The router does per-provider token-bucket rate limiting (`rateLimiter` at the bottom of `router.go`), exponential-backoff retries (`MaxRetries`/`RetryDelayMs` from provider config), and optional fallback provider/model via `RouteOptions`. **Every request, response, error, and fallback emits an audit event** — do not bypass the router for LLM calls.

7. **`internal/audit/audit.go`** writes to a Postgres `audit_log` table that the documented design assumes is granted INSERT+SELECT only. When `PHALANX_AUDIT_HASH_CHAIN=true`, each row's `payload_hash` is `sha256(prev_hash | event_type | actor | canonical_payload | created_at)`, where `canonical_payload` is the `json.Marshal` round-trip of the payload (so the hash survives jsonb's key reordering) and `created_at` is truncated to microsecond precision (to survive Postgres timestamptz storage). `chainMu` serializes chained writes so concurrent callers can't race on `prev_hash`. `VerifyChain` walks ranges to detect tampering. `Logger.Log` intentionally never returns an error to callers — it prints to stderr and continues, so failed audit writes do not break a review.

### Skills, agents, providers

These three tables are the configuration model and they are different things:

- **Skill** (`skills/*.yaml`, table `skills`): the *what* — slug, name, version, `system_prompt`, `checklist_template`, optional `output_schema`. Skills are registered via `phalanx skill register` (or `make seed`) which POSTs to `/api/skills`. Versions are unique on `(slug, version)`.
- **LLM Provider** (`config/providers.yaml`, table `llm_providers`): the *where* — base URL, auth method, default model, `api_key_ref` (vault reference, not plaintext — resolved via `internal/secrets`).
- **Agent** (table `agents`): the binding — `(skill_id, provider_id)` plus `model_override`, `temperature`, `max_tokens`, `priority`, `enabled`, and a JSONB `config` that holds `FilePatterns` / `SkipIfNoMatch`. Agents are what the orchestrator iterates over; there can be many agents per skill (e.g. one cheap model + one expensive fallback).

Adding a built-in review dimension means dropping a YAML in `skills/`, registering it, and creating an agent that binds it to a provider. No code changes required.

### Skill output contract

`agent/runtime.go:parseResponse` is the only consumer of model output. It expects:

- A line `**Verdict:** pass|warn|fail|not_applicable` (case-insensitive, substring match).
- Checklist items shaped `- [x] ...` / `- [ ] ...` / `- [~] ...` / `- [-] ...` mapping to pass/fail/warn/na.
- An optional `### Findings` section. Each finding is a `####` heading shaped `#### {emoji?} {severity} {— | : | -} {title}` followed by `**File:**`, `**Issue:**`, `**Fix:**`, and optional `**Reference:**` lines. Severities are `critical | major | minor | suggestion | info`. The parser tolerates emoji prefixes, backticks around file paths, and `(lines 12-34)` / `(line 28)` / `L34` line markers — see `parseFindings` in `internal/agent/runtime.go`. Malformed entries are skipped, not fatal.

Skill `checklist_template`s use `{{verdict}}` and `{{...}}` placeholders that the model fills in — these are not Go templates, they are documentation for the LLM. If you change the verdict regex, checklist regex, or finding-heading regex, update every YAML in `skills/` to match.

### Database

Single migration `migrations/001_initial.sql` defines: `llm_providers`, `skills`, `context_documents`, `agents`, `agent_context` (m:n), `review_sessions`, `agent_reports`, `decisions`, `audit_log`. UUIDs everywhere via `pgcrypto.gen_random_uuid()`. There is no migration runner — `make migrate` just `psql -f`s the file.

### Configuration

`internal/config/config.go` reads everything from env vars with defaults. Key envs:

- `DATABASE_URL`, `REDIS_URL`, `PORT` (3100), `HOST`
- `PHALANX_API_TOKENS` (comma-separated bearer tokens; empty leaves the API open — LOCAL DEV ONLY)
- `PHALANX_CORS_ALLOWED_ORIGINS` (comma-separated; empty disables CORS, "*" allowed but not recommended)
- `PHALANX_QUEUE_CONCURRENCY` (max parallel agents per session, default 10)
- `PHALANX_AUDIT_HASH_CHAIN` (enables tamper-evident chaining)
- `GITHUB_TOKEN` / `GITHUB_WEBHOOK_SECRET` (HMAC-SHA256 over body, `X-Hub-Signature-256`)
- `GITLAB_TOKEN` / `GITLAB_WEBHOOK_SECRET` (shared-token, `X-Gitlab-Token`)
- `BITBUCKET_AUTH` / `BITBUCKET_WEBHOOK_UUID` (HTTP Basic for the API, per-webhook UUID via `X-Hook-UUID`)
- `ANTHROPIC_API_KEY`, `OPENAI_API_KEY`, `DEEPSEEK_API_KEY`

Config is loaded from env only; there is no config-file loader.

### Tests

Core packages have unit tests: `internal/agent` (parseResponse, matchGlob, estimateCost), `internal/report` (computeOverall, BuildComposite), `internal/secrets` (env/file/cache). Audit tests in `internal/audit` exercise the real hash chain and require a running Postgres — they **skip** unless `PHALANX_TEST_DATABASE_URL` is set, which keeps `go test ./...` green in a plain checkout. To run them: `PHALANX_TEST_DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx go test ./internal/audit`.

## CLI

`cmd/cli/main.go` is a Cobra app (`phalanx`) that just talks to the server's HTTP API. `phalanx review` auto-detects `GITHUB_ACTIONS`/`GITLAB_CI` env vars and falls back to local `git rev-parse HEAD` and `git merge-base HEAD origin/main` when SHAs aren't supplied — useful when running outside CI but expects an `origin/main` to exist.

## Conventions

- All shared domain types live in `internal/types/types.go` — touch this when adding new audit event types, verdicts, or platform kinds so every package agrees.
- Package doc comments at the top of each `.go` file describe the package's role; keep them current when responsibilities shift.
- The orchestrator and runtime use `o.db.Exec(ctx, ...)` without checking errors in several places (status updates, report inserts). This is deliberate — a failed status update should not abort an in-progress review — but be careful when refactoring not to silently swallow errors that *do* matter.
