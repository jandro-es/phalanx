# Phalanx — Outstanding Work

Status legend: `[ ]` pending · `[~]` in progress · `[x]` done

Audit completed 2026-04-29 against `main @ cf087fb`. Build, vet, and `go test ./...` were all green at the time of the review.

---

## P0 — Security blockers (must fix before any non-local deployment)

- [x] **P0.1** API authentication middleware. `internal/api/auth.go` adds `BearerAuth(...)` driven by `PHALANX_API_TOKENS` (CSV). Webhooks and `/health` bypass via `SkipPrefixes`. Empty list disables auth (local dev) and the server logs a warning. Tests: `internal/api/auth_test.go`.
- [x] **P0.2** Webhook signature verification. `VerifyGitHubSignature` (HMAC-SHA256, constant-time) and `VerifyGitLabToken` (constant-time shared token) wired into the webhook handlers. Empty secret disables verification. Tests cover success/missing/invalid-hex/wrong-digest paths and a 401 rejection path through the router.
- [x] **P0.3** Tighten CORS. `cmd/server/main.go` reads `PHALANX_CORS_ALLOWED_ORIGINS` (CSV); empty disables CORS entirely. Default header allow-list is `Authorization, Content-Type` only.

## P1 — Functional gaps in advertised features

- [x] **P1.1** Stub API handlers. `getAgent` joins with skills/providers and returns context bindings; `updateAgent`/`updateProvider` use `COALESCE`-based partial UPDATE so callers can patch arbitrary subsets; `updateSkill` upserts on `(slug, version)` and auto-bumps the version when omitted; `rerunReview` wipes prior reports, resets the session, and re-enqueues. Tests added for partial patch, not-found, version bump, rerun reset, and re-enqueue.
- [x] **P1.2** Findings parsing. `parseFindings` walks the `### Findings` section, splits on per-finding `####` headings, and pulls structured `**File:** / **Issue:** / **Fix:** / **Reference:**` fields. Tolerates emoji prefixes, backticks, "(lines X-Y)" / "(line X)" / "Lnn" markers, and `—`/`-`/`:` heading separators. Tests cover the canonical PHALANX-PLAN.md §7.1 shape, alt separators, missing section, and empty section.
- [x] **P1.3** Context-document management. Five new endpoints under `/api/contexts` (list/get/create/update/delete) with audit events; `phalanx context list|register|delete` CLI subcommand; dashboard `Contexts` page (creates + deletes; the index endpoint omits `content` so the list stays light). Tests: validation, round-trip GET/PUT/DELETE, body-omit on list. Docs updated to drop the old "no API yet" note.
- [x] **P1.4** Surface `PostReview` errors. New `report.failed` audit event (`types.AuditReportFailed`); orchestrator only emits `report.posted` on success and emits `report.failed` with the error string otherwise. The session itself still completes — agent reports are already persisted. Test added (skips without `PHALANX_TEST_DATABASE_URL`).
- [x] **P1.5** GitLab `FetchDiff` populates additions/deletions. New `countDiffLines` counts `+`/`-` content lines per hunk (skipping `+++`/`---` file markers); also populates `OldPath` on renames. Tests cover modified/added/renamed files plus four hunk shapes for the counter.
- [x] **P1.6** Dashboard token UI. New `Settings` page (`dashboard/src/pages/Settings.tsx`) with a token input that writes/clears `localStorage.phalanx_token`, plus a live connection probe that calls `/api/agents` so the user sees the 401/200/DB-status outcome immediately. `useApi` now also surfaces 401s as a clear "Settings → API token" message. Vite build verified.

## P2 — Robustness & test debt

- [x] **P2.1** `internal/queue` tests (miniredis). Cover enqueue → worker dispatch, NewServer/NewClient validation. asynq's retry scheduler runs on >1s intervals so we test "handler error doesn't crash worker" rather than the precise retry timing.
- [x] **P2.2** Rate limiter actually blocks. `acquire(ctx, providerID, rpm)` now waits ~`60/rpm`s for a token (capped at 1s per iteration) and returns `ctx.Err()` on cancellation. Router signature changed to propagate context cancellation. Tests cover full-bucket non-blocking, blocked-on-empty + ctx cancel, per-provider isolation, and rpm=0 default.
- [x] **P2.3** Cost table coverage. Added entries for Claude 4.5/4.6/4.7, GPT-4o family, o1/o1-mini, and deepseek-chat. New `PHALANX_MODEL_PRICING` env var lets operators add or override entries without a code change (`<model>=<input>/<output>` USD per 1M, comma-separated).
- [x] **P2.4** Removed the dead `scanSession` placeholder.
- [x] **P2.5** CI workflow. `.github/workflows/ci.yml` runs (a) build/vet/race-test against an ephemeral Postgres service so audit + handler integration tests don't skip, (b) golangci-lint, (c) dashboard typecheck + Vite build.

## P3 — Roadmap items

- [x] **P3.1** Bitbucket platform client. `BitbucketClient` implements `FetchDiff` (combining the `/diff` text + `/diffstat` JSON for accurate +/- counts), `PostReview` (PR comment), `VerifyUser` (HTTP Basic with `username:app_password`). New `bitbucketWebhook` handler verifies `X-Hook-UUID` against `BITBUCKET_WEBHOOK_UUID`. Wired into `cmd/server/main.go` so any `BITBUCKET_AUTH` opt-in registers the client. Tests: diff fetch with three file states (modified/added/renamed), comment post shape, 4xx propagation.
- [x] **P3.2** Helm chart completeness. New templates: `secret.yaml` (created in-chart unless `existingSecret` is set, with API tokens / GitHub / GitLab / Bitbucket / LLM keys), `configmap.yaml` (non-secret env wired via `envFrom`), `ingress.yaml` (multi-host TLS), `migration-job.yaml` + `migrations-configmap.yaml` (post-install/upgrade hook running the SQL via `psql`). `Chart.yaml` declares Bitnami Postgres + Redis as conditional dependencies. Deployment now also forwards `PHALANX_API_TOKENS`, `BITBUCKET_AUTH`, `BITBUCKET_WEBHOOK_UUID`, `DEEPSEEK_API_KEY`, and the GitLab webhook secret. New `make helm-package`/`helm-lint` targets mirror the repo's migrations into the chart (gitignored). CI gains a `helm` job running `helm lint` + `helm template`.

---

## Verification

- `go build ./...` clean
- `go vet ./...` clean
- `go test ./... -race -count=1 -p 1` against the docker-compose Postgres at `phalanx_test`: all 11 packages green (audit, orchestrator, api integration tests included). `-p 1` is required for the integration packages because they share a single test database via `freshDB()` TRUNCATEs and trample each other when run concurrently — pre-existing limitation, codified in the new CI workflow.
- `cd dashboard && npx tsc --noEmit && npm run build` clean
- `helm` not installed locally — chart validation is wired into the new CI `helm` job (`helm lint` + `helm template`).

## Note on the test-DB parallelism

The orchestrator + audit + api integration tests share one Postgres database and rely on `TRUNCATE ... RESTART IDENTITY CASCADE` for isolation. Running their packages in parallel races; `-p 1` is the simplest fix and what CI uses. A long-term cleanup would be to give each test its own schema or use temp databases, but that's outside the scope of this round.
