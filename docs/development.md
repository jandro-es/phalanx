# Development

Hacking on Phalanx itself — setting up the dev loop, understanding the code layout, running tests, and adding features.

- [Dev setup](#dev-setup)
- [Code layout](#code-layout)
- [The review lifecycle, end to end](#the-review-lifecycle-end-to-end)
- [Tests](#tests)
- [Adding a built-in skill](#adding-a-built-in-skill)
- [Adding an LLM adapter](#adding-an-llm-adapter)
- [Adding a platform client](#adding-a-platform-client)
- [The skill output contract](#the-skill-output-contract)
- [Database migrations](#database-migrations)
- [Building release artefacts](#building-release-artefacts)
- [Code style and conventions](#code-style-and-conventions)

---

## Dev setup

```bash
# Clone
git clone https://github.com/phalanx-ai/phalanx.git
cd phalanx

# Tooling
go version            # 1.23+
docker compose ls     # Docker Compose v2

# Datastores only (so you can run the server natively)
docker compose -f deploy/docker-compose.yml up -d postgres redis

# Build binaries
make build

# Run the server
export DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx
export REDIS_URL=redis://localhost:6379
export ANTHROPIC_API_KEY=...          # or OPENAI_API_KEY, DEEPSEEK_API_KEY
make run

# Seed skills + providers (in another terminal)
make seed
make seed-providers
```

For dashboard work, also:

```bash
cd dashboard
npm install
npm run dev        # Vite HMR on :3000
```

### Editor setup

Anything that understands `gopls` works. Useful settings:

- `go.testFlags: ["-race", "-count=1"]` — always run with the race detector, never rely on the test cache.
- `gopls.staticcheck: true` — catches more than plain `go vet`.
- For VS Code, install the Go extension + Tailwind CSS IntelliSense for the dashboard.

### Reloading the server on file change

There's no built-in file-watcher target, but `go run ./cmd/server` is fast enough (<1 s cold boot) that a simple script usually suffices:

```bash
find . -name "*.go" | entr -r go run ./cmd/server
```

Or use [`air`](https://github.com/air-verse/air) with a config that targets `cmd/server/main.go`.

---

## Code layout

```
phalanx/
├── cmd/
│   ├── server/main.go          # HTTP server + asynq worker boot
│   └── cli/main.go             # Cobra CLI (review, skill, provider, agent, audit)
│
├── internal/
│   ├── types/types.go          # All shared domain types — the vocabulary
│   ├── config/config.go        # env-var loader with defaults
│   ├── secrets/secrets.go      # vault://, env://, file:// resolver
│   │
│   ├── audit/audit.go          # Append-only audit log + hash chain
│   ├── llm/
│   │   ├── router.go           # LLMRouter: rate limit, retries, fallback
│   │   └── adapters/
│   │       ├── anthropic.go    # /v1/messages adapter
│   │       └── openai_compat.go # Chat Completions-shaped providers
│   │
│   ├── queue/queue.go          # asynq Client + Server, review:run tasks
│   ├── agent/runtime.go        # Per-agent execution: prompt, call, parse
│   ├── orchestrator/orch.go    # Session lifecycle, parallel fan-out
│   ├── report/builder.go       # Composite Markdown builder
│   ├── platform/platform.go    # GitHub + GitLab clients (fetch diff, post review)
│   └── api/handlers.go         # chi HTTP handlers
│
├── skills/                     # 10 built-in skill YAMLs (accessibility, security, …)
├── config/providers.yaml       # Default LLM provider definitions
├── migrations/001_initial.sql  # Single Postgres migration
│
├── deploy/
│   ├── Dockerfile              # distroless Go build
│   ├── Dockerfile.dashboard    # Node + serve
│   ├── docker-compose.yml      # postgres + redis + phalanx [+dashboard]
│   └── helm/phalanx/           # Helm chart for Kubernetes
│
├── integrations/
│   ├── github-action/action.yml
│   └── gitlab-template/.phalanx.gitlab-ci.yml
│
├── dashboard/                  # React 19 + Vite 6 + Tailwind 4 SPA
├── docs/                       # These docs
└── CLAUDE.md                   # Architecture orientation for AI tooling
```

`internal/types/types.go` is the single source of truth for entity shapes. If you're adding a field that touches the API, the DB, or the dashboard, start there.

---

## The review lifecycle, end to end

Worth walking through once — it's the single most-cohesive flow in the codebase.

### 1. Trigger — something hits the HTTP server

Three ways in:

| Caller | Endpoint | What happens |
|---|---|---|
| GitHub/GitLab webhook | `POST /api/webhooks/{github,gitlab}` | Handler parses the event, constructs a `types.ReviewSession`, inserts into `review_sessions`, writes `session.created` audit event, then enqueues |
| CI / operator / CLI | `POST /api/reviews` | Same as above but with an explicit request body |
| GitHub webhook ping (draft) | same as above | Ignored with `{"ignored": true, "reason": "draft PR"}` |

All three call `api.Handler.createSession()` and then `h.Enqueuer.EnqueueReview(ctx, session)`.

### 2. Enqueue — queue.Client → asynq → Redis

`queue.Client.EnqueueReview` marshals just the session ID into a `review:run` task and pushes it onto asynq's default queue. Session payload stays small — the worker loads the full session from Postgres when it runs. A `session.queued` audit event records the task ID and queue retry policy.

### 3. Dequeue — asynq worker picks it up

`queue.Server` is started in the same process by `cmd/server/main.go`. Its handler is a closure that:

1. Parses the task payload (just a session ID).
2. Calls the `sessionLoader` callback (`loadSessionByID` in `main.go`) to hydrate the full `types.ReviewSession` from `review_sessions`.
3. Invokes `orchestrator.ExecuteReview(ctx, session)`.

Returning a non-nil error from the handler tells asynq to retry with exponential backoff up to `MaxRetries`.

### 4. Orchestrate — orchestrator.ExecuteReview

The big function in `internal/orchestrator/orchestrator.go`:

1. `setStatus("running")` + `session.running` audit event.
2. **Fetch the diff** if the session has no `diff_snapshot`. Calls `platform.GitHubClient.FetchDiff` or `GitLabClient.FetchDiff` depending on `session.Platform`. Stores the diff and file tree back on the session row for reproducibility.
3. **Load agents** — joins `agents → skills → llm_providers` and eager-loads `context_documents` per agent. One DB round-trip.
4. **Fan out** — for each enabled agent, spawn a goroutine bounded by a semaphore (`maxPar`, default 10). Each goroutine calls `agent.Runtime.Execute(...)`.
5. **Collect results** — every goroutine writes its per-agent row into `agent_reports` as it completes, then pushes a copy into the shared `reports` slice under a mutex.
6. **Build the composite** — `report.Builder.BuildComposite(session, reports)` produces the Markdown + overall verdict.
7. **Update the session** — writes `composite_report`, `overall_verdict`, `completed_at`, and status `completed`.
8. **Post to the git platform** — `client.PostReview` posts a PR comment + check run (GitHub) or MR note (GitLab). Emits `report.posted` audit event.
9. **Final audit event** — `session.completed` with the agent count and verdict.

### 5. Per-agent — agent.Runtime.Execute

For each agent:

1. **File-pattern short-circuit.** If `agent.config.skipIfNoMatch` is set and none of the changed files match `filePatterns`, emit `agent.skipped`, return a placeholder report with `verdict=not_applicable`, and skip the LLM call.
2. **Assemble the prompt.** `buildSystemPrompt` concatenates `skill.SystemPrompt + skill.ChecklistTemplate + each context document`. The user message is the PR metadata + file list + diff.
3. **Hash the prompt.** `sha256(system | user)` — stored on the report row for reproducibility and cache keys.
4. **Route to the LLM.** `llm.Router.Route(...)` picks the adapter for the agent's provider, applies token-bucket rate limiting, retries with exponential backoff on transient errors, and optionally falls back to `fallbackProviderId`.
5. **Parse the response.** `parseResponse` extracts the verdict from `**Verdict:** ...` and checklist items from `- [x] ...` / `- [ ] ...` / `- [~] ...` / `- [-] ...` via regex. Findings are not parsed today — they're passed through as raw Markdown.
6. **Estimate cost.** Looks up the model in a hardcoded rate table.
7. **Return** an `agent.Result` with a fully populated `types.AgentReport`. The orchestrator persists it.

### 6. The report — report.Builder.BuildComposite

Pure function — no DB, no network. Takes the session and the agent reports, returns a `types.CompositeReport` with:

- The overall verdict (`fail > warn > pass`).
- A summary table (one row per agent).
- A `<details>` block per agent with its full Markdown.
- A session ID footer.

### Where audit events are written

| Event | Source |
|---|---|
| `session.created` | `api.Handler.createSession` |
| `session.queued` | `queue.Client.EnqueueReview` |
| `session.running` | `orchestrator.ExecuteReview` (twice — once on status flip, once with agent count) |
| `agent.started` | `agent.Runtime.Execute` |
| `agent.completed` / `agent.failed` / `agent.skipped` | same file |
| `llm.request` / `llm.response` / `llm.error` / `llm.fallback` | `llm.Router.Route` |
| `report.posted` | `orchestrator.ExecuteReview` |
| `session.completed` | `orchestrator.ExecuteReview` |
| `decision.*` | `api.Handler.submitDecision` |
| `config.*` | `api.Handler.createSkill` / `createAgent` / `createProvider` |

With `PHALANX_AUDIT_HASH_CHAIN=true`, every one of those is linked by a SHA-256 chain serialized through `chainMu`.

---

## Tests

```bash
# Run everything. Postgres-backed tests are skipped without PHALANX_TEST_DATABASE_URL.
make test

# Unit tests only (no integration)
go test ./internal/agent ./internal/report ./internal/secrets -race -count=1

# Integration tests — need a running Postgres
PHALANX_TEST_DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx \
  go test ./internal/audit -v -race -count=1

# Everything with race + integration
PHALANX_TEST_DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx \
  go test ./... -race -count=1
```

### Coverage snapshot

Run from the repo root with Postgres up:

```bash
PHALANX_TEST_DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx \
  go test ./... -race -count=1 -p 1 -coverprofile=coverage.out -covermode=atomic

go tool cover -func=coverage.out | tail -20    # per-function
go tool cover -html=coverage.out               # open an HTML report
```

Per-package numbers, approximate, from the last full run:

| Package | Coverage | What's exercised |
|---|---|---|
| `internal/config` | 100% | Env var parsing, defaults, bool/int edge cases |
| `internal/llm` (router) | 96% | Retries, fallback, rate limiter, negative-retries guard, error propagation |
| `internal/secrets` | 94% | `env://`, `file://`, plain, vault (single, multi-value, key-not-found, 403, custom mount), caching |
| `internal/agent` | 93% | `parseResponse`, `matchGlob`, `estimateCost`, full `Execute` path (happy, skip-if-no-match, LLM error, model override, context docs) |
| `internal/report` | 92% | `computeOverall`, `BuildComposite` markdown structure, `shortSHA`/`shortID` guards |
| `internal/orchestrator` | 92% | Happy path, all-pass/all-fail, per-agent error handling, disabled agents, no-agents edge case, diff-fetch, fetch-error aborts, context injection |
| `internal/llm/adapters` | 90% | Anthropic: request shape, system message extraction, multiple text blocks, non-text blocks, custom headers, 429. OpenAI: Bearer vs api-key vs none, base URL handling (trailing slash, `/v1` prefix), finish_reason mapping, 500 errors, empty choices |
| `internal/platform` | 90% | GitHub+GitLab: FetchDiff with file-list parsing, PostReview with verdict→conclusion mapping, VerifyUser, error propagation, URL-encoded project paths |
| `internal/api` | 72% | 29 handler tests: createSkill/Provider/Agent validation + upsert, list endpoints returning `[]` not `null`, triggerReview enqueues + persists + audits, webhook (GitHub+GitLab), session detail + 404, decisions round-trip, audit endpoints, health |
| `internal/audit` | 49% | Hash chain integration (valid + tampering detection); the non-chain `Log` path is exercised indirectly by the handler tests |
| `internal/queue` | 0% | Not covered — thin asynq wrapper, exercised end-to-end by docker compose |
| `cmd/cli`, `cmd/server` | 0% | Glue code; no tests |

**Total: ~62%** of statements. The business-logic packages average ~86%.

### Test gates

Two environment variables control which tests run:

- **`PHALANX_TEST_DATABASE_URL`** — required for `internal/api`, `internal/audit`, and `internal/orchestrator` (they need a real Postgres). Unset → those tests `t.Skip(...)` cleanly. Set to the Compose DB:

  ```bash
  export PHALANX_TEST_DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx
  ```

- **`-p 1`** — required when running `go test ./...` with the DB tests enabled, because multiple packages truncate the same database. Without it, the api and audit tests race:

  ```bash
  go test ./... -race -count=1 -p 1
  ```

CI should set `PHALANX_TEST_DATABASE_URL` and pass `-p 1`. Local iteration on a single package doesn't need `-p 1`:

```bash
PHALANX_TEST_DATABASE_URL=... go test ./internal/api -race -count=1
```

### What's still not covered

- **`internal/queue`** — asynq itself has upstream tests; our wrapper is mechanical (parse URL, marshal task, call asynq). A miniredis-based test would be ~40 lines and is a reasonable TODO.
- **`cmd/cli` and `cmd/server`** — pure glue code, testing them is low-value compared to end-to-end testing via `make build && ./bin/phalanx-server` + `./bin/phalanx review`.
- **Webhook signature verification** — the handlers don't verify `GITHUB_WEBHOOK_SECRET` / `GITLAB_WEBHOOK_SECRET` today. When that's added, add tests alongside.

When adding new code to a covered package, please add tests. When adding code to an uncovered one, please add the first one for it.

### Running integration tests in Docker

If you don't want to run Postgres on the host, boot just the DB:

```bash
docker compose -f deploy/docker-compose.yml up -d postgres
PHALANX_TEST_DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx \
  go test ./internal/audit -v
```

---

## Adding a built-in skill

End-to-end recipe — also see [Configuration → Writing a new skill](configuration.md#writing-a-new-skill--end-to-end) for the operational side.

1. **Write the YAML** under `skills/`:

   ```yaml
   slug: license-check
   name: License Compliance
   version: 1
   tags: [compliance, legal]

   system_prompt: |
     You review PRs for license compliance. For each new file under
     `src/`, verify:
     - An SPDX-License-Identifier header is present
     - The header matches the repository's SPDX license in LICENSE
     - No files vendored from an incompatible license are added

   checklist_template: |
     ## ⚖️ License Compliance

     **Verdict:** {{verdict}}

     ### Checklist
     - [{{spdx_header}}] All new source files have an SPDX header
     - [{{consistent_license}}] Headers match the repo-level LICENSE
     - [{{no_incompatible_vendor}}] No incompatibly-licensed code vendored

     ### Findings
     {{findings}}
   ```

2. **Register it with a running server:**

   ```bash
   ./bin/phalanx skill register skills/license-check.yaml --server http://localhost:3100
   ```

3. **Create an agent** that binds the skill to a provider — see [Configuration → Agents](configuration.md#creating-an-agent).

4. **(Optional) add an icon** in `internal/report/builder.go:skillIcons`:

   ```go
   var skillIcons = map[string]string{
       "accessibility": "♿", "security": "🔒", …
       "license-check": "⚖️",
   }
   ```

   If you skip this, the composite report will use the generic 🔍 icon for your skill.

5. **Unit-test the parser if you're changing the output contract.** If you invent a new verdict word or checklist status character, update `parseResponse` in `internal/agent/runtime.go` *and* extend `internal/agent/runtime_test.go`. The parser is regex-based — see [The skill output contract](#the-skill-output-contract) below.

6. **Add the YAML to `make seed`.** Nothing to do — `make seed` globs `skills/*.yaml`, so dropping the file in is enough.

---

## Adding an LLM adapter

The OpenAI-compatible adapter already covers most providers. Add a new adapter only if the target API speaks a genuinely different shape — e.g. AWS Bedrock's native API, Google Gemini's non-OpenAI endpoint, or a model that needs streaming.

1. **Create** `internal/llm/adapters/<name>.go`. Implement the `llm.Adapter` interface:

   ```go
   type Adapter interface {
       Complete(ctx context.Context, req types.LLMRequest, provider types.LLMProvider) (*types.LLMResponse, error)
   }
   ```

2. **Resolve the API key** via `secrets.Resolve(*provider.APIKeyRef)` — do NOT read env vars directly. The resolver handles `env://`, `file://`, and `vault://` uniformly.

3. **Map the request** from `types.LLMRequest` into whatever the target API expects. Split `role: "system"` messages if the target API has a top-level `system` field (Anthropic does this).

4. **Return a `*types.LLMResponse`** with `Content`, `InputTokens`, `OutputTokens`, `Model`, and `FinishReason` populated. Leave `LatencyMs` — the router sets it.

5. **Wire it up** in `cmd/server/main.go` inside `loadProviders`:

   ```go
   var adapter llm.Adapter = openaiAdapter
   switch {
   case strings.Contains(p.Name, "bedrock") || strings.Contains(p.BaseURL, "bedrock"):
       adapter = bedrockAdapter
   case strings.Contains(p.Name, "anthropic") || strings.Contains(p.BaseURL, "anthropic"):
       adapter = anthropicAdapter
   }
   router.RegisterProvider(p, adapter)
   ```

6. **Test it** with an `httptest.NewServer` that replies with a canned response for the target API shape.

7. **Add cost entries** if you want cost estimates. Edit `internal/agent/runtime.go:costTable`:

   ```go
   var costTable = map[string][2]float64{
       "claude-sonnet-4-20250514": {3, 15},
       "anthropic.claude-3-5-sonnet-bedrock": {3, 15},  // new Bedrock model
       ...
   }
   ```

---

## Adding a platform client

Today Phalanx supports GitHub and GitLab. To add Bitbucket (or Gitea, or Azure DevOps):

1. **Implement the `platform.Client` interface** in `internal/platform/platform.go` (or a new file):

   ```go
   type Client interface {
       FetchDiff(ctx context.Context, repo, baseSHA, headSHA string) (*DiffResult, error)
       PostReview(ctx context.Context, session types.ReviewSession, report types.CompositeReport) error
       VerifyUser(ctx context.Context, token string) (*UserInfo, error)
   }
   ```

2. **Add a `types.Platform` constant** in `internal/types/types.go` (e.g. `PlatformBitbucket = "bitbucket"`).

3. **Register it** in `cmd/server/main.go` if the token env var is present:

   ```go
   if cfg.BitbucketToken != "" {
       platforms[types.PlatformBitbucket] = platform.NewBitbucketClient(cfg.BitbucketToken, cfg.BitbucketURL)
   }
   ```

4. **Add the env vars** to `internal/config/config.go`.

5. **Add a webhook handler** in `internal/api/handlers.go` — copy the pattern from `githubWebhook`.

6. **Add the check constraint value** to the schema — `migrations/001_initial.sql` has `platform TEXT NOT NULL CHECK (platform IN ('github', 'gitlab', 'bitbucket'))`, so `bitbucket` is already allowed. Other values need a new migration.

---

## The skill output contract

The orchestrator/runtime feeds the raw LLM response into `parseResponse` in `internal/agent/runtime.go`. Two regexes govern everything:

```go
var verdictRe   = regexp.MustCompile(`(?i)\*\*Verdict:\*\*\s*(.+)`)
var checklistRe = regexp.MustCompile(`(?m)^- \[([ x~\-])\]\s*(.+)$`)
```

| Rule | Example |
|---|---|
| The first line matching `**Verdict:** <word>` wins. Case-insensitive. Substring match on `pass`, `fail`, `warn`, `not_applicable`/`n/a`. | `**Verdict:** PASS` → `pass` |
| Missing verdict → default `warn` | (silent) |
| Checklist items are lines starting `- [x] `, `- [ ] `, `- [~] `, `- [-] ` at the start of a line. Anything inside can be Markdown. | `- [~] Input validation on new endpoints` → status `warn` |
| Checklist item without a valid status character is ignored (not counted as "fail") | `- [] missing space` → dropped |

Two consequences for anyone writing skills:

1. **Don't invent new verdict vocabulary.** Stick to `pass`/`warn`/`fail`/`not_applicable`. If you need more granularity, use the checklist statuses — that's what they're for.
2. **Prompt the LLM to respect the format.** The server automatically appends:

   ```
   Replace {{verdict}} with: pass, warn, fail, or not_applicable
   Checklist items: [x]=pass, [ ]=fail, [~]=warn, [-]=N/A
   ```

   to every system prompt in `buildSystemPrompt`. If you change the output vocabulary, update that hint too.

### Parser tests

`internal/agent/runtime_test.go` has cases for each verdict word and each checklist status. **Add a case when you change the parser** — there's no schema validation to catch regressions otherwise.

---

## Database migrations

Single file today: `migrations/001_initial.sql`. Phalanx does not include a migration runner — `make migrate` just runs `psql $DATABASE_URL -f migrations/001_initial.sql`. The Compose Postgres auto-applies any `.sql` file in `docker-entrypoint-initdb.d` on first boot (the compose file mounts `migrations/` there).

### Adding a new migration

1. **Create** `migrations/002_<description>.sql`. Use plain DDL; no framework.

2. **Wrap it in a transaction:**

   ```sql
   BEGIN;
   ALTER TABLE agents ADD COLUMN fallback_provider_id UUID REFERENCES llm_providers(id);
   COMMIT;
   ```

3. **Make it idempotent** where possible — `CREATE TABLE IF NOT EXISTS`, `ADD COLUMN IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`. That way re-running `make migrate` is safe.

4. **Extend `make migrate`** — or just run it by hand:

   ```bash
   psql $DATABASE_URL -f migrations/002_add_fallback_provider.sql
   ```

5. **Update the types + queries** — see [Code layout](#code-layout) for where to edit `types.go` and the relevant handlers.

6. **Rebuild the Compose volume** for local dev — `docker compose down -v` wipes `pgdata` and the next `up -d` re-applies all migrations in order.

---

## Building release artefacts

```bash
# Linux/amd64 static binaries with debug info stripped
make build-linux

# Docker images
docker build -f deploy/Dockerfile           -t phalanx-server:dev .
docker build -f deploy/Dockerfile.dashboard -t phalanx-dashboard:dev .

# Helm install against a real cluster
helm install phalanx deploy/helm/phalanx
```

The main `Dockerfile` is a multi-stage build: `golang:1.23-alpine` for compilation, `distroless/static-debian12:nonroot` for the final image. The result is ~20 MB with no shell, no ssl libraries, and a non-root user — suitable for production.

---

## Code style and conventions

- **Formatting:** `gofmt` / `goimports`. CI runs `go vet ./...` as a gate.
- **Imports:** standard library, then third-party, then `github.com/phalanx-ai/phalanx/...`, separated by blank lines. goimports handles this automatically.
- **Package docs:** Every package has a doc comment at the top of one file. Keep them current when responsibilities shift.
- **Errors:** Use `fmt.Errorf("...: %w", err)` to wrap. The audit logger is the one place that *intentionally* swallows errors and writes to stderr — do not propagate audit write failures, they must not break a review.
- **No new dependencies without justification.** `go.mod` is deliberately small. Adding a library should solve a real problem that can't be done in a few dozen lines of stdlib.
- **JSON tags:** Entity types (`ReviewSession`, `AgentReport`, `Skill`, `Agent`, `LLMProvider`, `ApprovalDecision`, `AuditEntry`) use **snake_case** — they're read by the dashboard, CLI, GitHub Action, and GitLab template, all of which assume snake_case. Request types (`CreateAgentRequest`, `TriggerReviewRequest`, `SubmitDecisionRequest`) use **camelCase** — that's what the CLI/dashboard/action write.
- **DB access:** The orchestrator, handlers, and `loadSessionByID` use hand-written SQL via `pgxpool`. No ORM. When adding a query, mirror the column order exactly in `rows.Scan(...)` and initialise result slices as `[]T{}` (not `var xs []T`) so JSON-encoded empties come out as `[]` not `null`.
- **Parallelism:** Goroutines are bounded. The orchestrator uses a semaphore channel of size `maxPar`; the LLM router uses a token-bucket per provider. Do not spawn unbounded goroutines for per-request work.
- **Comments:** Default to none. Only comment the *why* for non-obvious invariants — hidden constraints, workarounds for a specific bug, behavior that would surprise a reader. Never comment the *what* that's already in the identifier names.

### Before opening a PR

```bash
go vet ./...
go test ./... -race -count=1
PHALANX_TEST_DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx \
  go test ./internal/audit -race -count=1
make build             # sanity-check binaries build
```

If you touched the dashboard, also:

```bash
cd dashboard
npm run build          # vite build must succeed
```

And if you touched the Docker images:

```bash
docker build -f deploy/Dockerfile -t phalanx-server:test .
docker build -f deploy/Dockerfile.dashboard -t phalanx-dashboard:test .
```

Commits should be atomic and the message should say *why*, not *what* — the diff already says what.
