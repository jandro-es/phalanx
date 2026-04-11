# Phalanx — AI-Powered Pull Request Review Platform

## Comprehensive Technical & Business Plan

**Version:** 1.0
**Date:** April 2026
**Classification:** Internal — Engineering & Board Review

---

## 1. Executive Summary

Phalanx is an extensible, model-agnostic platform that automates pull request reviews using specialised AI agents. Each agent evaluates a PR against a single quality criterion — accessibility, code complexity, test coverage, architecture alignment, security, etc. — and produces a structured Markdown report with a pass/fail checklist. Reports are presented to a human engineer who makes the final merge decision, with full audit logging for compliance.

**Why now:** AI coding agents (Cursor, Copilot Workspace, Devin, Claude Code) are generating an increasing volume of code. Human review capacity is the bottleneck. Phalanx doesn't replace human judgment — it scales the reviewer's ability to catch issues across dimensions no single person can hold in their head simultaneously.

**Key differentiators:**

- One agent per criterion — modular, auditable, independently tunable
- Model-agnostic — use Anthropic, OpenAI, DeepSeek, or self-hosted models interchangeably
- Pipeline-native — GitHub Actions and GitLab CI first-class integrations
- Audit-grade — every review, every approval, every model invocation is immutably logged

---

## 2. System Architecture

### 2.1 High-Level Overview

```
┌──────────────────────────────────────────────────────────────────────┐
│                        Git Platform (GitHub / GitLab)                │
│  PR opened / updated ──► Webhook ──► Phalanx Gateway               │
└──────────────────────────────────────────────────────────────────────┘
                                  │
                                  ▼
┌──────────────────────────────────────────────────────────────────────┐
│                         Phalanx Core                                │
│                                                                      │
│  ┌────────────┐   ┌──────────────┐   ┌──────────────────────────┐   │
│  │  Ingestion  │──►│  Orchestrator │──►│  Agent Pool               │   │
│  │  Service    │   │              │   │                          │   │
│  │ (diff, tree,│   │ Reads agent  │   │  ┌─────────────────────┐│   │
│  │  metadata)  │   │ registry,    │   │  │ Agent: Accessibility ││   │
│  └────────────┘   │ fans out     │   │  │  skill + llm + ctx   ││   │
│                    │ reviews in   │   │  └─────────────────────┘│   │
│                    │ parallel     │   │  ┌─────────────────────┐│   │
│                    │              │   │  │ Agent: Security      ││   │
│                    └──────────────┘   │  │  skill + llm + ctx   ││   │
│                           │          │  └─────────────────────┘│   │
│                           │          │  ┌─────────────────────┐│   │
│                           │          │  │ Agent: Complexity    ││   │
│                           │          │  │  skill + llm + ctx   ││   │
│                           │          │  └─────────────────────┘│   │
│                           │          │  ┌─────────────────────┐│   │
│                           │          │  │ Agent: Tests         ││   │
│                           │          │  │  skill + llm + ctx   ││   │
│                           │          │  └─────────────────────┘│   │
│                           │          │  ┌─────────────────────┐│   │
│                           │          │  │ Agent: Architecture  ││   │
│                           │          │  │  skill + llm + ctx   ││   │
│                           │          │  └─────────────────────┘│   │
│                           │          │         ...              │   │
│                           │          └──────────────────────────┘   │
│                           ▼                                         │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                    LLM Router                                │   │
│  │  Resolves agent's model reference to a provider endpoint     │   │
│  │  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌───────────────┐  │   │
│  │  │ Anthropic│ │  OpenAI  │ │ DeepSeek │ │ Self-hosted   │  │   │
│  │  │  (Claude)│ │  (GPT)   │ │          │ │ (Qwen, Gemma) │  │   │
│  │  └──────────┘ └──────────┘ └──────────┘ └───────────────┘  │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                           │                                         │
│                           ▼                                         │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                  Report Aggregator                           │   │
│  │  Collects all agent reports ──► Composite Markdown           │   │
│  │  Posts as PR comment / check run                             │   │
│  └──────────────────────────────────────────────────────────────┘   │
│                           │                                         │
│                           ▼                                         │
│  ┌──────────────────────────────────────────────────────────────┐   │
│  │                    Audit Store                               │   │
│  │  Immutable log of every review, approval, and model call     │   │
│  │  PostgreSQL + append-only audit table + optional S3 archive  │   │
│  └──────────────────────────────────────────────────────────────┘   │
└──────────────────────────────────────────────────────────────────────┘
```

### 2.2 Core Components

| Component | Responsibility | Technology |
|---|---|---|
| **Gateway** | Receives webhooks, authenticates, routes | Node.js / Fastify |
| **Ingestion Service** | Fetches diff, file tree, PR metadata via Git platform API | Node.js |
| **Orchestrator** | Reads agent registry, fans out reviews, collects results | Node.js, BullMQ |
| **Agent Runtime** | Executes a single agent (skill + LLM + context) | Isolated worker |
| **LLM Router** | Resolves model identifiers to provider endpoints, handles retries/fallbacks | Node.js |
| **Report Aggregator** | Merges agent reports into composite MD, posts to PR | Node.js |
| **Audit Store** | Immutable logging of all events | PostgreSQL |
| **Dashboard** | Web UI for configuration, audit trail browsing, approval flow | React |

### 2.3 Data Model (Core Entities)

```sql
-- LLM Provider configuration
CREATE TABLE llm_providers (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,               -- "anthropic", "openai", "self-hosted-qwen"
    base_url        TEXT NOT NULL,               -- "https://api.anthropic.com/v1"
    auth_method     TEXT NOT NULL,               -- "bearer", "api-key-header", "none"
    api_key_ref     TEXT,                        -- vault reference, never plaintext
    default_model   TEXT,                        -- "claude-sonnet-4-20250514"
    config_json     JSONB DEFAULT '{}',          -- rate limits, timeouts, custom headers
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

-- Review Skill (the "what to check" definition)
CREATE TABLE skills (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    slug            TEXT UNIQUE NOT NULL,         -- "accessibility", "security", "complexity"
    name            TEXT NOT NULL,
    version         INT NOT NULL DEFAULT 1,
    system_prompt   TEXT NOT NULL,                -- the skill prompt
    checklist_template TEXT,                      -- MD checklist template for output
    output_schema   JSONB,                        -- optional JSON schema for structured output
    is_builtin      BOOLEAN DEFAULT false,
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now(),
    UNIQUE(slug, version)
);

-- Additional context documents (guidelines, non-negotiables)
CREATE TABLE context_documents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    content         TEXT NOT NULL,
    doc_type        TEXT NOT NULL,                -- "guideline", "non-negotiable", "reference"
    created_at      TIMESTAMPTZ DEFAULT now()
);

-- Agent = Skill + LLM + Context
CREATE TABLE agents (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    skill_id        UUID NOT NULL REFERENCES skills(id),
    provider_id     UUID NOT NULL REFERENCES llm_providers(id),
    model_override  TEXT,                         -- override provider's default model
    temperature     REAL DEFAULT 0.0,
    max_tokens      INT DEFAULT 4096,
    enabled         BOOLEAN DEFAULT true,
    priority        INT DEFAULT 100,              -- execution order / importance
    created_at      TIMESTAMPTZ DEFAULT now(),
    updated_at      TIMESTAMPTZ DEFAULT now()
);

-- Agent ↔ Context many-to-many
CREATE TABLE agent_context (
    agent_id        UUID REFERENCES agents(id),
    context_id      UUID REFERENCES context_documents(id),
    PRIMARY KEY (agent_id, context_id)
);

-- A review session triggered by a PR event
CREATE TABLE review_sessions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    external_pr_id  TEXT NOT NULL,                -- "github:owner/repo#123"
    platform        TEXT NOT NULL,                -- "github", "gitlab"
    pr_title        TEXT,
    pr_author       TEXT,
    head_sha        TEXT NOT NULL,
    base_sha        TEXT NOT NULL,
    diff_snapshot    TEXT,                         -- stored for reproducibility
    status          TEXT DEFAULT 'pending',        -- pending, running, completed, failed
    composite_report TEXT,                         -- final aggregated MD report
    started_at      TIMESTAMPTZ DEFAULT now(),
    completed_at    TIMESTAMPTZ
);

-- Individual agent report within a session
CREATE TABLE agent_reports (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES review_sessions(id),
    agent_id        UUID NOT NULL REFERENCES agents(id),
    skill_slug      TEXT NOT NULL,
    model_used      TEXT NOT NULL,                -- exact model string used
    provider_name   TEXT NOT NULL,
    prompt_hash     TEXT NOT NULL,                -- SHA-256 of full prompt sent
    input_tokens    INT,
    output_tokens   INT,
    latency_ms      INT,
    raw_response    TEXT NOT NULL,                -- full model response
    report_md       TEXT NOT NULL,                -- parsed MD report
    checklist_json  JSONB,                        -- structured checklist results
    verdict         TEXT NOT NULL,                -- "pass", "warn", "fail"
    created_at      TIMESTAMPTZ DEFAULT now()
);

-- Human approval / rejection decisions
CREATE TABLE approval_decisions (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    session_id      UUID NOT NULL REFERENCES review_sessions(id),
    decision        TEXT NOT NULL,                -- "approve", "request_changes", "defer"
    engineer_id     TEXT NOT NULL,                -- SSO / Git platform user ID
    engineer_name   TEXT NOT NULL,
    engineer_email  TEXT,
    justification   TEXT,                         -- optional reason
    overridden_verdicts JSONB,                    -- any agent verdicts the engineer overrode
    decided_at      TIMESTAMPTZ DEFAULT now()
);

-- Immutable append-only audit log
CREATE TABLE audit_log (
    id              BIGSERIAL PRIMARY KEY,
    event_type      TEXT NOT NULL,                -- see audit events list
    session_id      UUID,
    agent_id        UUID,
    actor           TEXT,                         -- system, engineer ID, or "webhook"
    payload         JSONB NOT NULL,
    created_at      TIMESTAMPTZ DEFAULT now()
);
-- No UPDATE or DELETE permissions granted on audit_log
```

**Audit event types:** `session.created`, `session.completed`, `agent.started`, `agent.completed`, `agent.failed`, `llm.request`, `llm.response`, `report.posted`, `decision.approve`, `decision.request_changes`, `decision.defer`, `config.agent.created`, `config.agent.updated`, `config.skill.updated`, `config.provider.updated`.

---

## 3. Agent Architecture (Modular Design)

### 3.1 Agent Composition

Every agent is a triple of **(Skill, LLM, Context[])**:

```
Agent "accessibility-review"
├── Skill: accessibility (v3)
│   ├── system_prompt: "You are an accessibility expert reviewing code changes..."
│   ├── checklist_template: "## Accessibility Review\n- [ ] ARIA attributes..."
│   └── output_schema: { verdict, findings[], checklist[] }
├── LLM: anthropic / claude-sonnet-4-20250514
│   ├── temperature: 0.0
│   └── max_tokens: 4096
└── Context:
    ├── WCAG 2.2 AA quick-reference (guideline)
    └── Company accessibility non-negotiables (non-negotiable)
```

### 3.2 Skill Definition Format

Skills are stored in the database but can also be defined as YAML files for version control:

```yaml
# skills/accessibility.yaml
slug: accessibility
name: Accessibility Review
version: 3

system_prompt: |
  You are a senior accessibility engineer reviewing a pull request diff.
  
  Your task is to evaluate the changes against WCAG 2.2 Level AA compliance
  and the project's accessibility guidelines provided in the context.
  
  For each issue found, provide:
  1. Severity: critical / major / minor / suggestion
  2. Location: file path and approximate line range
  3. Issue: what is wrong
  4. Fix: how to fix it
  5. WCAG criterion: which success criterion is violated
  
  Output your review using the checklist template provided.
  
  If the PR does not touch any UI, markup, or user-facing content,
  state "Not applicable — no UI changes detected" and mark all items as N/A.

checklist_template: |
  ## Accessibility Review
  
  **Verdict:** {{verdict}}
  
  ### Checklist
  - [{{semantic_html}}] Semantic HTML used correctly
  - [{{aria_attributes}}] ARIA attributes present and valid
  - [{{keyboard_nav}}] Keyboard navigation preserved
  - [{{color_contrast}}] Color contrast meets AA ratio (4.5:1 text, 3:1 large)
  - [{{focus_management}}] Focus management handled on route/state changes
  - [{{alt_text}}] Images have meaningful alt text
  - [{{form_labels}}] Form inputs have associated labels
  - [{{motion_respect}}] Animations respect prefers-reduced-motion
  
  ### Findings
  {{findings}}

output_schema:
  type: object
  properties:
    verdict:
      type: string
      enum: [pass, warn, fail, not_applicable]
    checklist:
      type: array
      items:
        type: object
        properties:
          item: { type: string }
          status: { type: string, enum: [pass, fail, na] }
    findings:
      type: array
      items:
        type: object
        properties:
          severity: { type: string }
          file: { type: string }
          lines: { type: string }
          issue: { type: string }
          fix: { type: string }
          wcag_criterion: { type: string }
```

### 3.3 Adding a New Agent (The Extension Story)

Adding a new review criterion requires **zero code changes**. An engineer:

1. Writes a skill YAML (or uses the Dashboard UI)
2. Registers it: `phalanx skill register ./skills/my-new-check.yaml`
3. Creates an agent binding: `phalanx agent create --skill my-new-check --provider anthropic --model claude-sonnet-4-20250514`
4. The next PR triggers the new agent automatically

### 3.4 Built-in Agents (Ships Out of the Box)

| Agent | Skill | Default Model | What It Checks |
|---|---|---|---|
| **Accessibility** | `accessibility` | Claude Sonnet | WCAG 2.2 AA, ARIA, keyboard nav, contrast |
| **Security** | `security` | Claude Sonnet | OWASP Top 10, injection, auth, secrets, deps |
| **Code Complexity** | `complexity` | Claude Sonnet | Cyclomatic complexity, deep nesting, long functions, cognitive load |
| **Architecture** | `architecture` | Claude Sonnet | Layer violations, dependency direction, pattern compliance |
| **Test Coverage** | `test-coverage` | Claude Sonnet | Missing test cases, edge cases, mutation risk |
| **API Contract** | `api-contract` | Claude Sonnet | Breaking changes, versioning, backward compat |
| **Performance** | `performance` | Claude Sonnet | N+1 queries, unbounded loops, memory leaks, missing pagination |
| **Documentation** | `documentation` | Claude Sonnet | Missing/stale docs, changelog, JSDoc/docstrings |
| **Error Handling** | `error-handling` | Claude Sonnet | Uncaught exceptions, missing error boundaries, silent failures |
| **Code Style** | `code-style` | Claude Haiku | Naming conventions, dead code, consistency (fast model is sufficient) |

---

## 4. LLM Router — Provider Abstraction

### 4.1 Provider Configuration

```yaml
# config/providers.yaml
providers:
  - name: anthropic
    base_url: https://api.anthropic.com/v1
    auth_method: api-key-header
    api_key_ref: vault://secrets/anthropic-api-key
    default_model: claude-sonnet-4-20250514
    models:
      - claude-sonnet-4-20250514
      - claude-haiku-4-5-20251001
      - claude-opus-4-6
    rate_limit:
      requests_per_minute: 60
      tokens_per_minute: 400000

  - name: openai
    base_url: https://api.openai.com/v1
    auth_method: bearer
    api_key_ref: vault://secrets/openai-api-key
    default_model: gpt-4.1
    models:
      - gpt-4.1
      - gpt-4.1-mini
      - o3

  - name: deepseek
    base_url: https://api.deepseek.com/v1
    auth_method: bearer
    api_key_ref: vault://secrets/deepseek-api-key
    default_model: deepseek-r1

  - name: self-hosted-qwen
    base_url: https://llm-internal.company.com/v1
    auth_method: bearer
    api_key_ref: vault://secrets/internal-llm-key
    default_model: qwen-3.5-72b
    config:
      timeout_ms: 120000  # self-hosted may be slower
```

### 4.2 Router Logic

```typescript
interface LLMRequest {
  provider: string;
  model: string;
  messages: Message[];
  temperature: number;
  max_tokens: number;
  response_format?: { type: "json_object" };
}

interface LLMResponse {
  content: string;
  model: string;
  input_tokens: number;
  output_tokens: number;
  latency_ms: number;
  provider: string;
}

class LLMRouter {
  // All providers implement OpenAI-compatible /chat/completions
  // except Anthropic which uses /messages — router normalises both

  async route(request: LLMRequest): Promise<LLMResponse> {
    const provider = this.providers.get(request.provider);
    const adapter = this.getAdapter(provider);  // AnthropicAdapter | OpenAICompatAdapter
    
    const start = Date.now();
    const response = await adapter.complete(request);
    const latency = Date.now() - start;
    
    // Log to audit
    await this.audit.log("llm.request", { provider: request.provider, model: request.model });
    await this.audit.log("llm.response", { 
      provider: request.provider,
      model: response.model,
      input_tokens: response.input_tokens,
      output_tokens: response.output_tokens,
      latency_ms: latency,
    });
    
    return { ...response, latency_ms: latency, provider: request.provider };
  }
}
```

### 4.3 Adapter Pattern

Two adapter families cover the entire market:

- **AnthropicAdapter** — Maps to `/v1/messages` with `x-api-key` header, handles Anthropic's content block format
- **OpenAICompatibleAdapter** — Maps to `/v1/chat/completions` with Bearer auth. Covers OpenAI, DeepSeek, vLLM-hosted Qwen/Gemma/Minimax, Ollama, LiteLLM, and any OpenAI-compatible endpoint

Adding a genuinely new protocol (rare) means adding one adapter class — no changes to agents, skills, or orchestration.

---

## 5. Git Platform Integration

### 5.1 GitHub Integration

**Trigger:** GitHub Actions workflow or webhook-based.

```yaml
# .github/workflows/phalanx-review.yml
name: Phalanx PR Review
on:
  pull_request:
    types: [opened, synchronize, ready_for_review]

jobs:
  phalanx:
    runs-on: ubuntu-latest
    permissions:
      pull-requests: write
      checks: write
      contents: read
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      - name: Run Phalanx Review
        uses: phalanx-ai/action@v1
        with:
          phalanx_url: ${{ secrets.SENTINEL_URL }}
          phalanx_token: ${{ secrets.SENTINEL_TOKEN }}
          agents: "accessibility,security,complexity,architecture,test-coverage"
          fail_on: "fail"        # block merge on any "fail" verdict
          warn_on: "warn"        # post warning but don't block
```

**What the action does:**
1. Computes the diff between `base` and `head`
2. Sends diff + file tree + PR metadata to Phalanx API
3. Phalanx orchestrates agents in parallel
4. Action polls for completion (or uses webhook callback)
5. Posts composite report as a PR comment
6. Creates a GitHub Check Run with pass/fail status
7. If any agent verdict is "fail", the check fails → merge is blocked (if branch protection requires it)

### 5.2 GitLab Integration

```yaml
# .gitlab-ci.yml
phalanx-review:
  stage: review
  image: phalanx-ai/cli:latest
  rules:
    - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
  script:
    - phalanx review
        --gitlab-url $CI_SERVER_URL
        --project-id $CI_PROJECT_ID
        --mr-iid $CI_MERGE_REQUEST_IID
        --token $SENTINEL_TOKEN
  artifacts:
    reports:
      codequality: phalanx-report.json
    paths:
      - phalanx-report.md
```

### 5.3 Self-Hosted / On-Premise Deployment

For companies that cannot send code to an external service:

```
┌─────────────────────────────────────────┐
│         Company Network                  │
│                                          │
│  ┌────────────┐     ┌────────────────┐  │
│  │ GitLab     │────►│ Phalanx       │  │
│  │ (on-prem)  │     │ (on-prem)      │  │
│  └────────────┘     │                │  │
│                      │   ┌──────────┐│  │
│                      │   │ vLLM /   ││  │
│                      │   │ Ollama   ││  │
│                      │   │ (Qwen,   ││  │
│                      │   │  Gemma)  ││  │
│                      │   └──────────┘│  │
│                      └────────────────┘  │
└─────────────────────────────────────────┘
      No code leaves the network
```

Deployment options: Docker Compose, Helm chart, or single-binary with embedded SQLite for small teams.

---

## 6. Audit & Compliance

### 6.1 What Gets Logged

Every action in the system produces an immutable audit record:

| Event | Data Captured |
|---|---|
| **Review triggered** | PR ID, commit SHAs, diff hash, timestamp, trigger source |
| **Agent executed** | Agent ID, skill version, model used, full prompt hash, token counts, latency |
| **LLM call** | Provider, model, input/output token counts, latency, cost estimate |
| **Report generated** | Full Markdown report, structured checklist JSON, verdict |
| **Report posted** | Platform (GitHub/GitLab), comment ID, check run ID |
| **Engineer decision** | Engineer identity (SSO-verified), decision, justification, any overridden verdicts |
| **Configuration change** | Who changed what, before/after values |

### 6.2 Immutability Guarantees

- The `audit_log` table has no `UPDATE` or `DELETE` grants at the database role level
- Application code only uses `INSERT` — no ORM methods for mutation exist
- Optional: write-ahead to an append-only object store (S3/GCS) for tamper evidence
- Optional: hash-chain each record (each row includes `prev_hash`) for integrity verification

### 6.3 Retention & Export

- Default retention: 3 years (configurable per regulatory requirement)
- Export formats: JSON-lines, CSV, or direct SQL query access
- Dashboard provides filtered audit trail browsing with search by PR, engineer, date range, agent, or verdict

### 6.4 Engineer Approval Flow

```
PR reviewed by Phalanx
        │
        ▼
Engineer views composite report
        │
        ├── Agrees with all verdicts ──► Clicks "Approve" ──► Logged
        │
        ├── Overrides a verdict ──► Must provide justification ──► Logged with override detail
        │
        └── Requests changes ──► Logged ──► PR returns to development
```

The engineer's identity is captured from their Git platform SSO session. No anonymous approvals are possible.

---

## 7. Report Format

### 7.1 Individual Agent Report (Example)

```markdown
## 🔒 Security Review

**Agent:** security-review | **Model:** claude-sonnet-4-20250514 | **Duration:** 3.2s
**Verdict:** ⚠️ WARN

### Checklist
- [x] No hardcoded secrets or API keys
- [x] SQL queries use parameterised statements
- [ ] Input validation on new endpoint `/api/v2/upload`
- [x] Authentication required on all new routes
- [x] No known vulnerable dependencies introduced
- [x] CORS configuration unchanged
- [ ] Rate limiting not configured for new endpoint
- [x] No sensitive data in logs

### Findings

#### ⚠️ Major — Missing input validation
**File:** `src/api/upload.ts` (lines 34-52)
**Issue:** The `fileName` parameter is used directly in a file path without sanitisation.
This could allow path traversal attacks.
**Fix:** Sanitise with `path.basename()` and validate against an allowlist of extensions.

#### 💡 Suggestion — Rate limiting
**File:** `src/api/upload.ts` (line 28)
**Issue:** The `/api/v2/upload` endpoint has no rate limiting.
File upload endpoints are common targets for abuse.
**Fix:** Apply the existing `rateLimiter` middleware with a conservative limit (e.g., 10 req/min).

---
*Review ID: `b7e3f1a2` | Session: `a1c4d8e0` | Prompt hash: `sha256:9f3a...`*
```

### 7.2 Composite Report (Posted to PR)

```markdown
# 🛡️ Phalanx Review — PR #342

**Commit:** `a1b2c3d` | **Reviewed at:** 2026-04-11 14:32 UTC
**Overall:** ⚠️ 1 warning, 8 passes

| Agent | Verdict | Key Finding |
|---|---|---|
| 🔒 Security | ⚠️ WARN | Missing input validation on upload endpoint |
| ♿ Accessibility | ✅ PASS | No UI changes |
| 📐 Architecture | ✅ PASS | Follows repository layer pattern |
| 🧪 Test Coverage | ✅ PASS | New endpoint has unit + integration tests |
| ⚡ Performance | ✅ PASS | No concerns |
| 📖 Documentation | ✅ PASS | API docs updated |
| 🧩 Complexity | ✅ PASS | All functions under threshold |
| 🚨 Error Handling | ✅ PASS | Errors propagated correctly |

<details>
<summary>🔒 Security Review (click to expand)</summary>

[full security agent report here]

</details>

<details>
<summary>♿ Accessibility Review (click to expand)</summary>

[full accessibility agent report here]

</details>

[... remaining agents ...]

---
*Phalanx v1.2.0 | Session `a1c4d8e0` | [View full audit trail](https://phalanx.internal/sessions/a1c4d8e0)*
```

---

## 8. Technical Implementation Details

### 8.1 Technology Stack

| Layer | Choice | Rationale |
|---|---|---|
| **Runtime** | Node.js 22 (LTS) | Async I/O for parallel LLM calls; TypeScript for type safety |
| **API Framework** | Fastify | Low overhead, schema validation, plugin architecture |
| **Queue** | BullMQ (Redis-backed) | Fan-out agent execution, retries, dead-letter queue |
| **Database** | PostgreSQL 16 | JSONB for flexible schemas, strong audit guarantees |
| **Cache** | Redis 7 | Queue backend, rate limiting, response caching |
| **CLI** | Node.js (Commander) | `phalanx` CLI for local/CI usage |
| **Dashboard** | React + Tailwind | Audit browsing, config management, approval UI |
| **Container** | Docker | Single-container (Compose) or Kubernetes (Helm) |
| **Secrets** | HashiCorp Vault / env vars | API keys never stored in plaintext in DB |

### 8.2 Project Structure

```
phalanx/
├── packages/
│   ├── core/                    # Shared types, schemas, utilities
│   │   ├── src/
│   │   │   ├── types.ts         # Agent, Skill, Provider, Report types
│   │   │   ├── schemas.ts       # Zod schemas for validation
│   │   │   └── audit.ts         # Audit logger interface
│   │   └── package.json
│   │
│   ├── server/                  # API server
│   │   ├── src/
│   │   │   ├── server.ts        # Fastify app setup
│   │   │   ├── routes/
│   │   │   │   ├── webhooks.ts  # GitHub/GitLab webhook handlers
│   │   │   │   ├── reviews.ts   # Review session API
│   │   │   │   ├── agents.ts    # Agent CRUD API
│   │   │   │   ├── skills.ts    # Skill CRUD API
│   │   │   │   ├── providers.ts # Provider config API
│   │   │   │   ├── decisions.ts # Approval/rejection API
│   │   │   │   └── audit.ts     # Audit trail query API
│   │   │   ├── services/
│   │   │   │   ├── ingestion.ts      # Diff/metadata fetching
│   │   │   │   ├── orchestrator.ts   # Agent fan-out & collection
│   │   │   │   ├── agent-runtime.ts  # Single agent execution
│   │   │   │   ├── llm-router.ts     # Provider routing
│   │   │   │   ├── adapters/
│   │   │   │   │   ├── anthropic.ts
│   │   │   │   │   └── openai-compat.ts
│   │   │   │   ├── report-builder.ts # MD report generation
│   │   │   │   ├── git-platforms/
│   │   │   │   │   ├── github.ts     # GitHub API client
│   │   │   │   │   └── gitlab.ts     # GitLab API client
│   │   │   │   └── audit-store.ts    # Audit persistence
│   │   │   └── workers/
│   │   │       └── agent-worker.ts   # BullMQ worker
│   │   └── package.json
│   │
│   ├── cli/                     # CLI tool
│   │   ├── src/
│   │   │   ├── cli.ts
│   │   │   └── commands/
│   │   │       ├── review.ts    # Trigger review from CLI
│   │   │       ├── skill.ts     # Manage skills
│   │   │       ├── agent.ts     # Manage agents
│   │   │       └── audit.ts     # Query audit trail
│   │   └── package.json
│   │
│   └── dashboard/               # Web UI
│       ├── src/
│       │   ├── App.tsx
│       │   └── pages/
│       │       ├── Sessions.tsx
│       │       ├── AuditTrail.tsx
│       │       ├── AgentConfig.tsx
│       │       └── ProviderConfig.tsx
│       └── package.json
│
├── skills/                      # Built-in skill definitions (YAML)
│   ├── accessibility.yaml
│   ├── security.yaml
│   ├── complexity.yaml
│   ├── architecture.yaml
│   ├── test-coverage.yaml
│   ├── api-contract.yaml
│   ├── performance.yaml
│   ├── documentation.yaml
│   ├── error-handling.yaml
│   └── code-style.yaml
│
├── integrations/
│   ├── github-action/           # GitHub Action wrapper
│   │   └── action.yml
│   └── gitlab-template/         # GitLab CI template
│       └── .phalanx.gitlab-ci.yml
│
├── deploy/
│   ├── docker-compose.yml
│   ├── Dockerfile
│   └── helm/
│       └── phalanx/
│
├── migrations/                  # PostgreSQL migrations
│   ├── 001_initial.sql
│   └── 002_audit_log.sql
│
└── phalanx.config.yaml         # Default configuration
```

### 8.3 Agent Execution Flow (Detailed)

```typescript
// Simplified orchestrator logic
async function executeReview(session: ReviewSession): Promise<CompositeReport> {
  const agents = await db.agents.findEnabled();
  const diff = session.diff_snapshot;
  const metadata = { title: session.pr_title, author: session.pr_author };

  // Fan out all agents in parallel
  const reportPromises = agents.map(agent =>
    agentQueue.add("execute-agent", {
      sessionId: session.id,
      agentId: agent.id,
      diff,
      metadata,
    })
  );

  const reports = await Promise.allSettled(reportPromises);

  // Collect results, handle failures gracefully
  const agentReports: AgentReport[] = [];
  for (const result of reports) {
    if (result.status === "fulfilled") {
      agentReports.push(result.value);
    } else {
      agentReports.push(createFailureReport(result.reason));
    }
  }

  // Build composite report
  const composite = buildCompositeReport(session, agentReports);

  // Persist
  await db.reviewSessions.update(session.id, {
    status: "completed",
    composite_report: composite.markdown,
    completed_at: new Date(),
  });

  // Post to Git platform
  await postReportToPR(session, composite);

  return composite;
}

// Single agent execution (runs in BullMQ worker)
async function executeAgent(job: AgentJob): Promise<AgentReport> {
  const { sessionId, agentId, diff, metadata } = job.data;
  const agent = await db.agents.findById(agentId);
  const skill = await db.skills.findById(agent.skill_id);
  const contexts = await db.agentContext.findByAgentId(agentId);

  // Build the prompt
  const systemPrompt = [
    skill.system_prompt,
    "\n\n## Output Format\n",
    skill.checklist_template,
    ...contexts.map(c => `\n\n## ${c.doc_type}: ${c.name}\n${c.content}`),
  ].join("");

  const userMessage = [
    `## Pull Request: ${metadata.title}`,
    `**Author:** ${metadata.author}`,
    `\n## Diff\n\`\`\`diff\n${diff}\n\`\`\``,
  ].join("\n");

  const promptHash = sha256(systemPrompt + userMessage);

  // Call LLM via router
  const response = await llmRouter.route({
    provider: agent.provider.name,
    model: agent.model_override || agent.provider.default_model,
    messages: [
      { role: "system", content: systemPrompt },
      { role: "user", content: userMessage },
    ],
    temperature: agent.temperature,
    max_tokens: agent.max_tokens,
  });

  // Parse and validate response
  const parsed = parseAgentResponse(response.content, skill.output_schema);

  // Persist report
  const report = await db.agentReports.create({
    session_id: sessionId,
    agent_id: agentId,
    skill_slug: skill.slug,
    model_used: response.model,
    provider_name: response.provider,
    prompt_hash: promptHash,
    input_tokens: response.input_tokens,
    output_tokens: response.output_tokens,
    latency_ms: response.latency_ms,
    raw_response: response.content,
    report_md: parsed.markdown,
    checklist_json: parsed.checklist,
    verdict: parsed.verdict,
  });

  return report;
}
```

### 8.4 Context Window Management

Large PRs may exceed model context limits. The Ingestion Service handles this:

1. **Prioritise changed files** — always include the full diff
2. **Include referenced files** — files imported by changed files, loaded on demand
3. **Chunking strategy** — for diffs exceeding 80% of context window, split by file group and run the agent multiple times, then merge verdicts (most-severe wins)
4. **Token counting** — use `tiktoken` (OpenAI) or Anthropic's token counting API to pre-check fit

---

## 9. Security Considerations

| Concern | Mitigation |
|---|---|
| API key exposure | Keys stored in Vault; never in DB or logs; referenced by vault path |
| Code exfiltration | On-prem deployment option; self-hosted models keep code in-network |
| Prompt injection via code | Agent system prompts include explicit instruction boundaries; output is parsed, not executed |
| Audit tampering | Append-only table with no UPDATE/DELETE grants; optional hash-chain |
| Unauthorised approvals | Approvals require SSO-verified identity from Git platform OAuth |
| Rate-limit abuse | BullMQ concurrency limits; per-provider rate limiting in LLM Router |

---

## 10. Implementation Plan

### Phase 0 — Foundation (Weeks 1–3)

| Week | Deliverable |
|---|---|
| 1 | Monorepo scaffolding, DB migrations, core types/schemas, Fastify server skeleton |
| 2 | LLM Router with Anthropic + OpenAI-compatible adapters, integration tests |
| 3 | Agent runtime: skill loading, prompt assembly, response parsing, report generation |

**Exit criteria:** A single agent can be invoked via API with a hardcoded diff and returns a valid Markdown report.

### Phase 1 — Core Platform (Weeks 4–7)

| Week | Deliverable |
|---|---|
| 4 | Orchestrator: parallel fan-out, composite report builder, verdict aggregation |
| 5 | GitHub integration: webhook handler, diff fetching via API, PR comment posting, Check Run API |
| 6 | Audit store: append-only logging, all event types, audit query API |
| 7 | Built-in skills: write and test all 10 default skill prompts with real PRs |

**Exit criteria:** A PR on GitHub triggers a full review with 10 agents, posts a composite report, and creates a check run. All events are logged.

### Phase 2 — Production Readiness (Weeks 8–10)

| Week | Deliverable |
|---|---|
| 8 | Approval flow: decision API, engineer identity capture, override justification |
| 9 | GitLab integration, CLI tool, Docker Compose deployment, Helm chart |
| 10 | Dashboard: session browser, audit trail viewer, agent/provider configuration UI |

**Exit criteria:** Full end-to-end flow on both GitHub and GitLab. Dashboard operational. Docker deployment tested.

### Phase 3 — Hardening & Scale (Weeks 11–13)

| Week | Deliverable |
|---|---|
| 11 | Context window management (chunking, token counting), large-PR handling |
| 12 | Rate limiting, retry logic, fallback models, cost estimation/budgeting |
| 13 | Load testing, security audit, documentation, internal dogfooding |

**Exit criteria:** System handles 100 concurrent reviews. Documented. Security-reviewed.

### Post-Launch Roadmap

| Quarter | Capability |
|---|---|
| Q3 2026 | Bitbucket integration, Slack/Teams notifications, custom webhook destinations |
| Q4 2026 | Review analytics dashboard (trends, common issues, agent accuracy tracking) |
| Q1 2027 | Agent calibration: compare agent verdicts against human reviewer decisions to tune prompts |
| Q2 2027 | IDE integration (VS Code extension) for pre-push reviews |

---

## 11. Cost Analysis

### 11.1 Per-Review Cost Estimate

Assumptions: Average PR diff is ~2,000 tokens; 10 agents per review; using Claude Sonnet 4.

| Item | Tokens | Unit Cost | Cost |
|---|---|---|---|
| Input (system prompt + diff + context) × 10 agents | ~50,000 | $3 / 1M input | $0.15 |
| Output (report) × 10 agents | ~20,000 | $15 / 1M output | $0.30 |
| **Total per review** | | | **~$0.45** |

At 500 PRs/month: **~$225/month** in LLM costs.

Using self-hosted models (Qwen 3.5 on company GPU): LLM cost drops to infrastructure cost only.

### 11.2 Infrastructure Costs

| Component | Monthly Cost (Cloud) |
|---|---|
| Phalanx server (2 vCPU, 4GB) | $40 |
| PostgreSQL (managed, small) | $30 |
| Redis (managed, small) | $15 |
| **Total infrastructure** | **~$85/month** |

### 11.3 Development Investment

| Phase | Duration | FTE | Cost (at $150k/yr fully loaded) |
|---|---|---|---|
| Phase 0–1 (MVP) | 7 weeks | 2 engineers | ~$40,000 |
| Phase 2 (Production) | 3 weeks | 2 engineers | ~$17,000 |
| Phase 3 (Hardening) | 3 weeks | 2 engineers | ~$17,000 |
| **Total to production** | **13 weeks** | | **~$74,000** |

---

## 12. Risk Register

| Risk | Probability | Impact | Mitigation |
|---|---|---|---|
| LLM hallucinations produce false positives/negatives | Medium | Medium | Human always makes final decision; agent accuracy tracked over time; prompt tuning based on feedback |
| Context window too small for large PRs | Medium | Low | Chunking strategy; summarisation pass; file prioritisation |
| Model API outage blocks CI pipeline | Low | High | Fallback model configuration; timeout with graceful degradation (review marked "incomplete") |
| Engineers ignore AI reviews | Medium | Medium | Start with advisory mode; track engagement; tune signal-to-noise ratio |
| Regulatory requirement for specific audit format | Low | Medium | Audit export is format-agnostic (JSON-lines); custom formatters easy to add |
| Prompt injection via malicious code in PR | Low | High | Output is parsed/rendered as Markdown only, never executed; agent prompts include boundary instructions |

---

## 13. Success Metrics

| Metric | Target (6 months) | Measurement |
|---|---|---|
| Review coverage | 100% of PRs reviewed by Phalanx | Webhook trigger rate |
| False positive rate | < 15% of "fail" verdicts overridden by engineers | Override tracking in audit log |
| Time to review | < 60 seconds for 90th percentile | Agent latency tracking |
| Engineer adoption | > 80% of engineers read reports before merge | Dashboard engagement analytics |
| Defect escape rate | 20% reduction in production incidents from code issues | Incident correlation (manual initially) |
| Audit compliance | 100% of merge decisions have logged reviewer identity | Audit log completeness check |

---

## Appendix A — Glossary

| Term | Definition |
|---|---|
| **Agent** | A configured unit that evaluates a PR against one criterion. Composed of a Skill + LLM + optional Context. |
| **Skill** | The prompt, checklist template, and output schema that define what an agent checks. |
| **Context** | Supplementary documents (guidelines, non-negotiables) injected into an agent's prompt. |
| **Provider** | An LLM API endpoint (Anthropic, OpenAI, self-hosted, etc.) |
| **Review Session** | A single evaluation of a PR, comprising multiple agent reports. |
| **Verdict** | An agent's conclusion: pass, warn, fail, or not_applicable. |
| **Composite Report** | The aggregated Markdown document combining all agent reports. |

---

## Appendix B — Approval for Proceeding

This plan requires:

1. **Engineering allocation:** 2 full-time engineers for 13 weeks
2. **Infrastructure budget:** ~$85/month (cloud) or existing on-prem capacity
3. **LLM API budget:** ~$225/month at 500 PRs/month (scales linearly; reducible via self-hosted models)
4. **Total investment to production:** ~$74,000 in engineering + ~$1,000 in infrastructure/LLM costs during development

**Expected ROI:** Conservative estimate of 4 engineering-hours saved per PR review across the team (reduced context-switching, fewer review rounds, fewer escaped defects). At 500 PRs/month and $75/hr fully loaded engineering cost, that is $150,000/month in recovered capacity — a payback period under 1 month.

---

*Document prepared for engineering and board review. For questions, contact the platform engineering team.*
