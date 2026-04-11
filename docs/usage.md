# Usage

Day-to-day operation: triggering reviews, reading the results, submitting decisions, and querying the audit trail. This doc assumes you already have at least one provider and one enabled agent — if not, start with [Configuration](configuration.md).

- [Triggering reviews](#triggering-reviews)
- [Reading a review session](#reading-a-review-session)
- [Submitting a decision](#submitting-a-decision)
- [Querying the audit trail](#querying-the-audit-trail)
- [Verifying the hash chain](#verifying-the-hash-chain)
- [Listing and filtering sessions](#listing-and-filtering-sessions)
- [Full API reference](#full-api-reference)

---

## Triggering reviews

There are four ways to start a review. All of them end up creating the same `review_sessions` row and enqueueing the same `review:run` task on asynq.

| Method | Who uses it | Payload |
|---|---|---|
| **GitHub Action** | CI on `pull_request` events | Auto-derived from the event |
| **GitLab CI template** | CI on `merge_request_event` | Auto-derived from `$CI_*` vars |
| **Webhook** | GitHub/GitLab pushing directly to Phalanx | Derived from webhook payload |
| **CLI / API** | Operators, scripts, backfill jobs | Supplied explicitly |

The first three are covered in the deployment docs ([GitHub](deployment-github.md), [GitLab](deployment-gitlab.md)). This section covers the direct API/CLI path, which is what everything else ultimately calls.

### Via the CLI

```bash
./bin/phalanx review \
  --server http://localhost:3100 \
  --token $PHALANX_TOKEN \
  --repo acme/widget \
  --pr 42
```

Most flags are optional. Inside GitHub Actions or GitLab CI the CLI auto-detects `GITHUB_REPOSITORY`, `GITHUB_SHA`, `CI_PROJECT_PATH`, `CI_COMMIT_SHA`, and falls back to `git rev-parse HEAD` + `git merge-base HEAD origin/main` when nothing else is set.

Common flags:

| Flag | Default | Purpose |
|---|---|---|
| `--platform` | `github` | `github` or `gitlab` |
| `--repo` | auto | `owner/name` |
| `--pr` | — | PR / MR number |
| `--head` | auto | Head SHA |
| `--base` | auto | Base SHA |
| `--wait` | false | Poll until the review completes |
| `--timeout` | `120` | Seconds to wait when `--wait` is set |
| `--fail-on` | `fail` | Exit non-zero on `fail`, `warn`, or `none` |
| `--output` | — | Write the composite Markdown report to this path |

End-to-end example:

```bash
./bin/phalanx review --repo acme/widget --pr 42 --wait --output /tmp/report.md

🛡️  Phalanx Review
   Repo: acme/widget
   PR: #42
   abc1234..def5678

✅ Session: 3fd59aa0-2ed1-4639-885d-fdbb3a459c1b
⏳ Waiting...

📋 Verdict: WARN
📝 Report: /tmp/report.md
```

### Via the raw API

`POST /api/reviews`:

```bash
curl -sS -X POST http://localhost:3100/api/reviews \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer $PHALANX_TOKEN" \
  -d '{
    "platform": "github",
    "repository": "acme/widget",
    "prNumber": 42,
    "headSha": "abc1234567890abcdef1234567890abcdef12345",
    "baseSha": "fedcba0987654321fedcba0987654321fedcba09",
    "triggerSource": "api"
  }'
```

Returns immediately with `202 Accepted`:

```json
{
  "estimatedDurationMs": 30000,
  "sessionId": "3fd59aa0-2ed1-4639-885d-fdbb3a459c1b",
  "status": "queued"
}
```

The request body uses **camelCase**. Required fields: `platform`, `repository`, `prNumber`, `headSha`, `baseSha`, `triggerSource`.

#### Passing the diff inline

If the server can't reach your git host (air-gapped runners, private forks without a token), send the unified diff in the body and the server will skip its own fetch:

```bash
DIFF=$(git diff origin/main...HEAD)

curl -sS -X POST http://localhost:3100/api/reviews \
  -H 'Content-Type: application/json' \
  -d "$(jq -n --arg diff "$DIFF" '{
    platform: "github",
    repository: "acme/widget",
    prNumber: 42,
    headSha: "abc1234",
    baseSha: "fedcba0",
    diff: $diff,
    triggerSource: "cli"
  }')"
```

The `diff` field is stored on the session row and reused by every agent without re-fetching.

#### Scoping to a subset of agents

The `agents` field (array of skill slugs) lets you trigger only the listed agents — useful for testing a single skill without disabling the rest:

```json
{
  "platform": "github",
  "repository": "acme/widget",
  "prNumber": 42,
  "headSha": "...", "baseSha": "...",
  "agents": ["security", "dependency-injection"],
  "triggerSource": "api"
}
```

*Note: this is accepted in the request schema but currently advisory — the orchestrator runs all enabled agents. Filtering by slug is on the roadmap; see the inline comment in `internal/orchestrator/orchestrator.go`.*

---

## Reading a review session

`GET /api/reviews/:sessionId` returns the full session, including the composite report and each per-agent detail.

```bash
curl -s http://localhost:3100/api/reviews/3fd59aa0-2ed1-4639-885d-fdbb3a459c1b | jq
```

```json
{
  "session": {
    "id": "3fd59aa0-...",
    "external_pr_id": "github:acme/widget#42",
    "platform": "github",
    "repository_full_name": "acme/widget",
    "pr_number": 42,
    "pr_title": "Add rate limiting to /login",
    "pr_author": "bob",
    "head_sha": "abc1234",
    "base_sha": "fedcba0",
    "status": "completed",
    "overall_verdict": "warn",
    "composite_report": "# 🛡️ Phalanx Review — PR #42\n\n**Add rate limiting to /login** | Commit: `abc1234` …",
    "started_at": "2026-04-11T16:50:23Z",
    "completed_at": "2026-04-11T16:50:47Z"
  },
  "reports": [
    {
      "id": "…",
      "skill_slug": "security",
      "model_used": "claude-sonnet-4-20250514",
      "provider_name": "anthropic",
      "verdict": "pass",
      "input_tokens": 4523,
      "output_tokens": 612,
      "latency_ms": 8231,
      "cost_estimate_usd": 0.022635,
      "report_md": "## 🔒 Security Review\n\n**Verdict:** pass\n…",
      "findings": [ … ]
    },
    { "skill_slug": "performance", "verdict": "warn", … }
  ],
  "decisions": [],
  "progress": { "completed": 10, "total": 10 }
}
```

### Session lifecycle states

| `status` | Meaning |
|---|---|
| `pending` | Row created, not yet handed off to the queue |
| `queued` | Enqueued on asynq, waiting for a worker slot |
| `running` | Worker is executing agents |
| `completed` | All agents finished; composite report written |
| `failed` | Orchestrator errored out before completion — check audit events |
| `cancelled` | Reserved; not emitted by current code paths |

Poll `/api/reviews/:id` every few seconds from a CI job or from the dashboard. The `progress.completed / progress.total` fields let you render a progress bar; `total` is the count of enabled agents.

### Getting the composite Markdown on its own

The composite is stored on the session row as `composite_report`. To extract it:

```bash
curl -s http://localhost:3100/api/reviews/<id> | jq -r '.session.composite_report' > report.md
```

The GitHub Action does exactly this and uploads the file as a workflow artifact.

---

## Submitting a decision

A human engineer's decision on a completed review. Lives in the `approval_decisions` table.

### Decision types

| Decision | Meaning |
|---|---|
| `approve` | Merge allowed, even if agents flagged warnings |
| `request_changes` | Author must address findings before merge |
| `defer` | Escalate / leave for someone else; review stays open |

### Submitting

```bash
curl -sS -X POST http://localhost:3100/api/decisions/3fd59aa0-2ed1-4639-885d-fdbb3a459c1b \
  -H 'Content-Type: application/json' \
  -d '{
    "decision": "approve",
    "engineerId": "alice@acme.com",
    "engineerName": "Alice Zhou",
    "engineerEmail": "alice@acme.com",
    "justification": "The `warn` from performance was acknowledged — the slow query is behind a feature flag."
  }'
```

Fields:

| Field | Required | Notes |
|---|---|---|
| `decision` | ✅ | `approve` / `request_changes` / `defer` |
| `engineerId` | ✅ | Stable identifier — your SSO subject id, email, username |
| `engineerName` | ✅ | Display name (for dashboard) |
| `engineerEmail` | | Contact email |
| `justification` | | Required if you're overriding an agent verdict |
| `overriddenVerdicts` | | Array of `{agentReportId, skillSlug, originalVerdict, overriddenTo, reason}` — use when approving despite a `fail` |

Every submitted decision writes an `audit_log` row with event type `decision.approve` / `decision.request_changes` / `decision.defer`, actor = `engineerId`, and the justification in the payload.

### Overriding a failing verdict

Say the `security` agent flagged `fail` but the engineer determines it's a false positive. They can approve the review *and* record an override, which leaves a breadcrumb in the audit log:

```bash
curl -X POST http://localhost:3100/api/decisions/<sessionId> \
  -H 'Content-Type: application/json' \
  -d '{
    "decision": "approve",
    "engineerId": "alice@acme.com",
    "engineerName": "Alice",
    "justification": "False positive on the injection check — the input is already parameterised via $1.",
    "overriddenVerdicts": [
      {
        "agentReportId": "<uuid of the security report>",
        "skillSlug": "security",
        "originalVerdict": "fail",
        "overriddenTo": "pass",
        "reason": "False positive: prepared statement already in use at line 42."
      }
    ]
  }'
```

### Retrieving decisions

```bash
# All decisions on a session
curl -s http://localhost:3100/api/decisions/<sessionId> | jq

# All decisions by an engineer (useful for review leaderboards / audit)
curl -s http://localhost:3100/api/decisions/by-engineer/alice@acme.com | jq
```

---

## Querying the audit trail

Every state change emits an audit event. Events are immutable (the table is insert-only by convention) and optionally hash-chained.

### Event types

Session lifecycle: `session.created`, `session.queued`, `session.running`, `session.completed`, `session.failed`
Agent lifecycle: `agent.started`, `agent.completed`, `agent.failed`, `agent.skipped`
LLM: `llm.request`, `llm.response`, `llm.error`, `llm.fallback`
Reports: `report.posted`
Decisions: `decision.approve`, `decision.request_changes`, `decision.defer`
Config: `config.agent.created`, `config.agent.updated`, `config.skill.created`, `config.skill.updated`, `config.provider.created`, `config.provider.updated`

### Querying

```bash
# All events, newest first (default 100)
curl -s 'http://localhost:3100/api/audit?limit=200' | jq

# Events for a specific session, chronological
curl -s http://localhost:3100/api/audit/session/3fd59aa0-2ed1-4639-885d-fdbb3a459c1b | jq
```

Filters (via query string):

| Param | Example | Effect |
|---|---|---|
| `sessionId` | `sessionId=3fd59aa0-...` | Only events on that session |
| `eventType` | `eventType=llm.error` | Only matching event type |
| `actor` | `actor=alice@acme.com` | Only events from a given actor |
| `limit` | `limit=500` | Page size (default 100) |
| `offset` | `offset=500` | Skip N rows |

### From the CLI

```bash
./bin/phalanx audit trail 3fd59aa0-2ed1-4639-885d-fdbb3a459c1b
```

Outputs the raw JSON — pipe through `jq` for filtering.

### Exporting as JSON-lines

Useful for shipping to a SIEM or long-term archive:

```bash
curl -s http://localhost:3100/api/audit/export > audit-$(date +%F).jsonl
```

Each line is a standalone JSON object. `wc -l` gives the row count.

---

## Verifying the hash chain

If you booted the server with `PHALANX_AUDIT_HASH_CHAIN=true`, every audit row carries a `payload_hash` that links to the previous row's hash. Tampering with any row (or gap in the chain) invalidates everything that follows.

### Verify the full log

```bash
curl -s http://localhost:3100/api/audit/verify | jq
```

```json
{ "valid": true, "checkedCount": 2431 }
```

Or from the CLI:

```bash
./bin/phalanx audit verify
```

### Verify a range

```bash
curl -s 'http://localhost:3100/api/audit/verify?from=1000&to=2000' | jq
```

### What happens if the chain is broken

```json
{
  "valid": false,
  "checkedCount": 422,
  "firstBrokenId": 423
}
```

`firstBrokenId` is the first row where the recomputed hash disagrees with the stored `payload_hash`. Everything from that row onwards is also invalid (chained verification). Investigate:

1. Look at the broken row directly:
   ```bash
   docker exec deploy-postgres-1 psql -U phalanx -d phalanx -c "SELECT * FROM audit_log WHERE id = 423"
   ```
2. Check the row immediately before it — the broken row's `prev_hash` should match the previous row's `payload_hash`.
3. If someone ran a manual `UPDATE audit_log SET ...` you'll see the payload/timestamp doesn't match.

The canonical JSON + microsecond-truncated timestamps used by `Logger.logWithChain` mean false positives are impossible: if verification fails, the row genuinely differs from what was written.

---

## Listing and filtering sessions

```bash
# Most-recent 20 sessions
curl -s http://localhost:3100/api/reviews | jq

# Paginated
curl -s 'http://localhost:3100/api/reviews?limit=50&offset=100' | jq
```

Each entry has the minimum fields you need to render a dashboard row:

```json
{
  "id": "...",
  "external_pr_id": "github:acme/widget#42",
  "platform": "github",
  "repository_full_name": "acme/widget",
  "pr_number": 42,
  "pr_title": "Add rate limiting to /login",
  "pr_author": "bob",
  "head_sha": "abc1234",
  "base_sha": "fedcba0",
  "status": "completed",
  "overall_verdict": "warn",
  "started_at": "2026-04-11T16:50:23Z",
  "completed_at": "2026-04-11T16:50:47Z"
}
```

---

## Full API reference

| Method | Path | Purpose |
|---|---|---|
| POST | `/api/webhooks/github` | GitHub webhook receiver |
| POST | `/api/webhooks/gitlab` | GitLab webhook receiver |
| POST | `/api/reviews` | Trigger a review |
| GET | `/api/reviews` | List sessions (paginated) |
| GET | `/api/reviews/:sessionId` | Session detail (session, reports, decisions, progress) |
| POST | `/api/reviews/:sessionId/rerun` | Re-run a session (stub — tracks) |
| POST | `/api/decisions/:sessionId` | Submit a human decision |
| GET | `/api/decisions/:sessionId` | Decisions on a session |
| GET | `/api/decisions/by-engineer/:engineerId` | Decisions by engineer |
| GET | `/api/agents` | List agents (joined with skill + provider names) |
| POST | `/api/agents` | Create an agent |
| GET | `/api/agents/:id` | Agent detail |
| PUT | `/api/agents/:id` | Update an agent |
| DELETE | `/api/agents/:id` | Disable (sets `enabled=false`; does not delete) |
| GET | `/api/skills` | List skills |
| POST | `/api/skills` | Register a skill (upsert on slug+version) |
| GET | `/api/skills/:slug` | Latest version of a skill |
| PUT | `/api/skills/:slug` | Update a skill |
| GET | `/api/providers` | List providers |
| POST | `/api/providers` | Register a provider (upsert on name) |
| PUT | `/api/providers/:id` | Update a provider |
| GET | `/api/audit` | Query the audit log (filtered) |
| GET | `/api/audit/session/:sessionId` | Full audit trail for a session |
| GET | `/api/audit/verify` | Verify the hash chain |
| GET | `/api/audit/export` | Export as JSON-lines |
| GET | `/health` | Liveness + DB ping |

### Request bodies are camelCase

Create / update endpoints expect camelCase (`baseUrl`, `prNumber`, `skillId`). Response bodies return snake_case (`base_url`, `pr_number`, `skill_id`). The JavaScript/TypeScript dashboard, the CLI, and the GitHub/GitLab integrations all assume this split.

### Authentication

There is no built-in auth. Put Phalanx behind a reverse proxy that terminates TLS and checks bearer tokens — see [Configuration → API tokens](configuration.md#api-tokens-optional).
