# Configuration

This is the doc for operators who want to change *what Phalanx reviews*, *how*, and *with which models*. Three concepts to understand up front, then step-by-step recipes for each.

- [The mental model: Skill vs Provider vs Agent](#the-mental-model)
- [Providers](#providers)
- [Skills](#skills)
- [Agents](#agents)
- [Context documents](#context-documents)
- [File-pattern scoping](#file-pattern-scoping)
- [Temperature, max tokens, fallback models](#temperature-max-tokens-fallback-models)
- [API tokens (optional)](#api-tokens-optional)

---

## The mental model

Three rows in three different tables, bound together at review time:

```
┌───────────┐     ┌───────────┐     ┌───────────┐
│   Skill   │     │  Provider │     │  Context  │
│           │     │           │     │ documents │
│ "what"    │     │ "where"   │     │           │
│           │     │           │     │ (optional)│
└─────┬─────┘     └─────┬─────┘     └─────┬─────┘
      │                 │                 │
      │    ┌────────────▼─────────────┐   │
      └───►│          Agent           │◄──┘
           │                          │
           │ "who" — runs the skill   │
           │  via the provider with   │
           │  these contexts attached │
           └──────────────────────────┘
```

- A **Skill** is a review dimension. It owns the system prompt and the checklist template. One skill = one "voice" that looks at diffs through one lens (security, accessibility, complexity, …). Skills are code-like — they're versioned and idempotent. The 10 built-in skills live in `skills/*.yaml`.
- A **Provider** is an LLM endpoint. It owns the base URL, auth method, default model, and rate limits. Anthropic, OpenAI, DeepSeek, self-hosted vLLM, and Ollama are all just different Provider rows.
- An **Agent** binds a skill to a provider with runtime settings — model override, temperature, max tokens, priority, and optional file-pattern scoping. Agents are the thing the orchestrator iterates over when a review fires.

Two critical consequences:

1. **You can have many agents per skill.** A common pattern is one "primary" agent using a cheap fast model and one "escalation" agent using a premium model for the same skill — or one agent per provider, to compare outputs.
2. **A skill is useless until an agent binds it to a provider.** The skills table can contain all 10 built-ins and the server will still do nothing on a review — you also need agents.

### Where things live in the database

| Table | What's in it |
|---|---|
| `skills` | slug, name, version, system_prompt, checklist_template, tags, is_builtin |
| `llm_providers` | name, base_url, auth_method, api_key_ref, default_model, models, config |
| `agents` | name, skill_id, provider_id, model_override, temperature, max_tokens, enabled, priority, config (JSONB) |
| `context_documents` | name, content, doc_type, tags |
| `agent_context` | agent_id ↔ context_id (many-to-many) |

Everything is CRUD-managed via `/api/{skills,agents,providers}` — the CLI and the dashboard are thin wrappers around the same endpoints.

---

## Providers

### What's in a provider

```yaml
# config/providers.yaml — matches the upstream format
- name: anthropic                              # unique key, referenced by agents
  base_url: https://api.anthropic.com/v1       # adapter-specific; /v1/messages for anthropic
  auth_method: api-key-header                  # api-key-header | bearer | none
  api_key_ref: env://ANTHROPIC_API_KEY         # resolved at request time; see below
  default_model: claude-sonnet-4-20250514      # used when an agent doesn't override
  models:                                      # informational; shown in dashboard
    - claude-sonnet-4-20250514
    - claude-haiku-4-5-20251001
    - claude-opus-4-6
  config:                                      # maps to types.ProviderConfig
    requests_per_minute: 60                    # token bucket rate limit
    tokens_per_minute: 400000                  # advisory; not enforced today
    timeout_ms: 60000                          # HTTP client timeout
    max_retries: 2                             # negative = default 2; 0 = disabled
    retry_delay_ms: 1000                       # base delay; exponential backoff per attempt
```

### Secret references

`api_key_ref` is **never** a plaintext key. It resolves to a real value at request time via `internal/secrets`:

| Reference | Source |
|---|---|
| `env://VAR_NAME` | Process environment variable |
| `file:///path/to/secret` | Local file (whitespace-trimmed). Useful in k8s with mounted secrets |
| `vault://kv-path` | HashiCorp Vault KV v2 at `$VAULT_ADDR` with `$VAULT_TOKEN`. Set `VAULT_KV_MOUNT` if not using the default `secret/` mount |
| `vault://kv-path#key` | Same, but returns the named field from a multi-value secret |
| `plain-string` | Treated as a literal value — **dev only**, logs will show it |

Values are cached in-memory for 5 minutes to avoid thrashing the secret store.

### Adapters

Phalanx ships two LLM adapters:

| Adapter | Used when | Endpoint shape |
|---|---|---|
| **Anthropic** | Provider name or `base_url` contains `anthropic` | `POST {base_url}/messages` with `x-api-key` header |
| **OpenAI-compatible** | Everything else | `POST {base_url}/chat/completions` (server appends `/v1/` if missing) with `Authorization: Bearer` or `api-key:` header |

The OpenAI adapter covers OpenAI, DeepSeek, Google Gemini's OpenAI-compatible endpoint, vLLM, Ollama, LiteLLM, Groq, Together, Fireworks — anything that speaks the Chat Completions API.

To add a new adapter shape (e.g. Bedrock's native API), see [Development → Adding an LLM adapter](development.md#adding-an-llm-adapter).

### Registering providers

**Bulk from the shipped YAML file (recommended):**

```bash
./bin/phalanx provider register config/providers.yaml --server http://localhost:3100
# or
make seed-providers
```

Both accept either the nested `providers:` list form (as in `config/providers.yaml`) or a single top-level provider object. The command is idempotent — re-registering updates the row in place using the provider name as the upsert key.

**One provider via curl:**

```bash
curl -sS -X POST http://localhost:3100/api/providers \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "openai",
    "baseUrl": "https://api.openai.com/v1",
    "authMethod": "bearer",
    "apiKeyRef": "env://OPENAI_API_KEY",
    "defaultModel": "gpt-4.1",
    "models": ["gpt-4.1", "gpt-4.1-mini", "o3", "o4-mini"],
    "config": {
      "requestsPerMinute": 500,
      "timeoutMs": 60000,
      "maxRetries": 2
    }
  }'
# {"id":"<uuid>","name":"openai"}
```

Note: JSON request bodies use **camelCase** (`baseUrl`, `authMethod`, `defaultModel`, `apiKeyRef`), while responses use **snake_case** (`base_url`, `auth_method`, `default_model`, `api_key_ref`) to match database column names.

**List providers:**

```bash
./bin/phalanx provider list --server http://localhost:3100
# or
curl -s http://localhost:3100/api/providers | jq
```

### Self-hosted example — vLLM

```yaml
- name: self-hosted-qwen
  base_url: https://llm-internal.company.com/v1
  auth_method: bearer
  api_key_ref: env://INTERNAL_LLM_KEY
  default_model: Qwen/Qwen3-235B-A22B
  models:
    - Qwen/Qwen3-235B-A22B
    - Qwen/Qwen3-32B
  config:
    timeout_ms: 180000         # self-hosted is often slower
    max_retries: 1             # local cluster; don't hammer it
```

### Self-hosted example — Ollama

```yaml
- name: ollama-local
  base_url: http://localhost:11434/v1
  auth_method: none            # Ollama has no auth out of the box
  default_model: gemma4:27b
  models:
    - gemma4:27b
    - qwen3:32b
  config:
    timeout_ms: 300000
    max_retries: 0
```

---

## Skills

### What's in a skill

A skill YAML file:

```yaml
slug: security                # unique + stable; referenced by agents
name: Security Review         # human display name
version: 1                    # bump to rev; uniqueness is (slug, version)
tags: [security, owasp, appsec]

system_prompt: |
  You are a senior application security engineer reviewing a pull request diff.

  Evaluate changes against OWASP Top 10 and CWE Top 25. For each issue include
  severity (critical/major/minor/suggestion), file location, issue description,
  fix, and relevant reference.

checklist_template: |
  ## 🔒 Security Review

  **Verdict:** {{verdict}}

  ### Checklist
  - [{{no_secrets}}]     No hardcoded secrets, API keys, or credentials
  - [{{sql_injection}}]  SQL queries use parameterised statements
  - [{{xss_prevention}}] User input escaped before HTML rendering
  - [{{input_validation}}] Input validation on new endpoints
  - [{{auth_required}}]  Authentication required on new routes

  ### Findings
  {{findings}}
```

Two important points:

1. **`{{verdict}}` and `{{xxx}}` placeholders are documentation for the LLM**, not Go templates. The server literally substitutes the template into the system prompt surrounding text, and the LLM is instructed to fill in the placeholders when it responds. If you rename a placeholder, nothing on the server cares — it only cares about the *verdict* line and *checklist* items in the final reply.
2. **The server parses the LLM response with regexes**, not a JSON schema. The output contract is:
   - A line `**Verdict:** pass|warn|fail|not_applicable` (case-insensitive, substring match).
   - Zero or more lines shaped `- [x] ...` / `- [ ] ...` / `- [~] ...` / `- [-] ...`. Meanings: `x`=pass, `space`=fail, `~`=warn, `-`=not_applicable.

   If you write a skill where the LLM's output doesn't match these patterns, the verdict will default to `warn` and the checklist will be empty. See [Development → The skill output contract](development.md#the-skill-output-contract) for where the regexes live.

### Registering a skill

**From the shipped YAMLs (recommended):**

```bash
make seed
```

Registers all 10 built-in skills under `skills/*.yaml`. Idempotent — upserts on `(slug, version)`.

**One skill:**

```bash
./bin/phalanx skill register skills/security.yaml --server http://localhost:3100
# ✅ Skill registered (security v1): {"id":"<uuid>","slug":"security","version":1}
```

**Via the API:**

```bash
curl -sS -X POST http://localhost:3100/api/skills \
  -H 'Content-Type: application/json' \
  -d '{
    "slug": "license-check",
    "name": "License Compliance",
    "version": 1,
    "systemPrompt": "You review PRs for license compliance...",
    "checklistTemplate": "## ⚖️ License\n\n**Verdict:** {{verdict}}\n\n### Checklist\n- [{{spdx_header}}] SPDX header on new files\n",
    "tags": ["compliance", "legal"]
  }'
```

### Writing a new skill — end to end

Let's add a skill that checks for dependency-injection anti-patterns in Go.

1. Create `skills/dependency-injection.yaml`:

   ```yaml
   slug: dependency-injection
   name: DI & Coupling Review
   version: 1
   tags: [architecture, golang]

   system_prompt: |
     You review Go pull requests for dependency-injection and coupling issues.

     Flag:
     - Functions that reach into package-level globals instead of taking
       dependencies as arguments.
     - Concrete types used in public APIs where an interface would be cheaper
       to mock and friendlier to swap.
     - Tight coupling between sibling packages (package A importing from B
       which imports from A).
     - Hidden side effects during `init()`.

     Do NOT flag:
     - main.go wiring code — it's allowed to reach for concrete types.
     - Test helpers.

   checklist_template: |
     ## 🔗 DI & Coupling Review

     **Verdict:** {{verdict}}

     ### Checklist
     - [{{no_globals}}] No new package-level mutable globals
     - [{{deps_as_args}}] New functions take their dependencies as arguments
     - [{{interfaces_at_boundaries}}] Public APIs prefer interfaces where a mock would be valuable
     - [{{no_init_side_effects}}] No hidden side effects in `init()`

     ### Findings
     {{findings}}
   ```

2. Register it:

   ```bash
   ./bin/phalanx skill register skills/dependency-injection.yaml --server http://localhost:3100
   ```

3. Bind it to a provider with an agent:

   ```bash
   SKILL_ID=$(curl -s http://localhost:3100/api/skills | jq -r '.skills[] | select(.slug=="dependency-injection") | .id')
   PROVIDER_ID=$(curl -s http://localhost:3100/api/providers | jq -r '.providers[] | select(.name=="anthropic") | .id')

   curl -sS -X POST http://localhost:3100/api/agents \
     -H 'Content-Type: application/json' \
     -d "{
       \"name\": \"di-claude\",
       \"skillId\": \"$SKILL_ID\",
       \"providerId\": \"$PROVIDER_ID\",
       \"modelOverride\": \"claude-sonnet-4-20250514\",
       \"temperature\": 0.0,
       \"maxTokens\": 4096,
       \"enabled\": true,
       \"priority\": 50,
       \"config\": {
         \"filePatterns\": [\"**/*.go\"],
         \"skipIfNoMatch\": true
       }
     }"
   ```

4. That's it. The next review will run the new agent on every PR that touches a `*.go` file.

---

## Agents

An agent is the thing that actually runs. The orchestrator does `SELECT * FROM agents WHERE enabled = true ORDER BY priority` and launches one goroutine per row.

### Agent fields

| Field | JSON (request) | Required | Default | Notes |
|---|---|---|---|---|
| Name | `name` | ✅ | — | Used in dashboard listings |
| Skill ID | `skillId` | ✅ | — | UUID of the skill |
| Provider ID | `providerId` | ✅ | — | UUID of the provider |
| Model override | `modelOverride` | | `null` | If set, overrides `provider.defaultModel` for this agent |
| Temperature | `temperature` | | `0.0` | Pinned to 0 by default — review agents should be deterministic |
| Max tokens | `maxTokens` | | `4096` | Response ceiling |
| Enabled | `enabled` | | `false` | **New agents default to disabled**; flip to `true` to have them run |
| Priority | `priority` | | `100` | Lower = runs earlier. Doesn't affect correctness, only log ordering |
| Config | `config` | | `{}` | See below — JSONB blob of runtime options |

### Agent config (JSONB)

The `config` field is a free-form `types.AgentConfig`:

```json
{
  "filePatterns": ["**/*.go", "**/*.ts"],
  "ignorePatterns": ["**/vendor/**", "**/*.generated.go"],
  "skipIfNoMatch": true,
  "maxDiffTokens": 120000,
  "fallbackProviderId": "<uuid>",
  "fallbackModel": "gpt-4.1-mini"
}
```

Fields:

- **`filePatterns`** — only run this agent if at least one file in the changeset matches one of these globs. See [File-pattern scoping](#file-pattern-scoping).
- **`ignorePatterns`** — reserved, not yet consumed by the runtime (tracked).
- **`skipIfNoMatch`** — when `true` and `filePatterns` is non-empty, an agent whose patterns don't match any file is skipped and records a `not_applicable` report. When `false`, the agent runs regardless. Default: `false`.
- **`maxDiffTokens`** — reserved, not yet consumed (today the router passes the full diff).
- **`fallbackProviderId` / `fallbackModel`** — if set, the LLM router will retry with this provider/model when the primary fails after all its retries. See [Temperature, max tokens, fallback models](#temperature-max-tokens-fallback-models).

### Creating an agent

**Via curl:**

```bash
SKILL_ID=$(curl -s http://localhost:3100/api/skills    | jq -r '.skills[] | select(.slug=="accessibility") | .id')
PROVIDER_ID=$(curl -s http://localhost:3100/api/providers | jq -r '.providers[] | select(.name=="openai") | .id')

curl -sS -X POST http://localhost:3100/api/agents \
  -H 'Content-Type: application/json' \
  -d "{
    \"name\": \"a11y-gpt4mini\",
    \"skillId\": \"$SKILL_ID\",
    \"providerId\": \"$PROVIDER_ID\",
    \"modelOverride\": \"gpt-4.1-mini\",
    \"temperature\": 0.0,
    \"maxTokens\": 2048,
    \"enabled\": true,
    \"priority\": 80,
    \"config\": {
      \"filePatterns\": [\"**/*.tsx\", \"**/*.jsx\", \"**/*.vue\", \"**/*.svelte\", \"**/*.html\"],
      \"skipIfNoMatch\": true
    }
  }"
```

**Via the dashboard:** Agents page → (feature coming soon — agent creation via dashboard is currently disabled; use the CLI/API for now).

### Disabling or re-enabling

```bash
# Disable by id
curl -X DELETE http://localhost:3100/api/agents/<id>    # sets enabled=false; row is not deleted
```

Re-enabling currently requires updating via SQL or via the (placeholder) `PUT /api/agents/:id` endpoint — the dashboard toggle hits that path.

### Recommended agent bundles

**"Standard review" — 10 agents, all skills, one provider**

Run `make seed` to load the 10 built-in skills, then create one agent per skill bound to your cheapest fast model. This is what the quickstart workflow gives you.

```bash
SKILLS="accessibility api-contract architecture code-style complexity documentation error-handling performance security test-coverage"
PROVIDER_ID=$(curl -s http://localhost:3100/api/providers | jq -r '.providers[] | select(.name=="anthropic") | .id')

for slug in $SKILLS; do
  SKILL_ID=$(curl -s http://localhost:3100/api/skills | jq -r ".skills[] | select(.slug==\"$slug\") | .id")
  curl -sS -X POST http://localhost:3100/api/agents \
    -H 'Content-Type: application/json' \
    -d "{
      \"name\": \"$slug-default\",
      \"skillId\": \"$SKILL_ID\",
      \"providerId\": \"$PROVIDER_ID\",
      \"modelOverride\": \"claude-haiku-4-5-20251001\",
      \"enabled\": true,
      \"priority\": 100
    }"
done
```

**"Security escalation" — two agents for one skill**

```bash
# Primary — cheap model, always runs
curl ... -d '{"name":"security-haiku", "modelOverride":"claude-haiku-4-5-20251001", "priority":10, ...}'

# Escalation — premium model, only runs on files that look risky
curl ... -d '{"name":"security-opus", "modelOverride":"claude-opus-4-6", "priority":20,
              "config":{"filePatterns":["**/auth/**", "**/crypto/**", "**/*password*"], "skipIfNoMatch":true}}'
```

Both agents are independent — they both write a report row and both feed the composite verdict. Useful when you want the premium model's opinion only for high-risk changes.

---

## Context documents

A **context document** is reference material appended to an agent's system prompt — think "your coding conventions", "links to the architecture RFC", "a list of deprecated APIs to flag". They're many-to-many with agents, so the same doc can be attached to multiple agents.

### Doc types

`doc_type` is free text but convention uses:

| Type | Meaning |
|---|---|
| `guideline` | "Prefer X over Y" — soft advice the agent should weigh |
| `non-negotiable` | Hard rule — violating it should force a `fail` |
| `reference` | Factual material — list of approved libraries, deprecated endpoints, etc. |
| `example` | Good / bad code examples |

### Adding a context document

Three options, in order of preference:

**1. Dashboard.** The "Contexts" tab lists, creates, and deletes context documents.

**2. CLI.** Write the document as YAML and register it:

```yaml
# acme-conventions.yaml
name: Acme coding conventions
doc_type: non-negotiable
tags: [golang, internal]
content: |
  All new Go code must follow the conventions in https://wiki.acme.com/go-style.

  Hard rules (violations => fail):
  - No `panic()` in library code; use error returns.
  - No logging of raw user input at info level (may contain PII).
```

```bash
phalanx context register acme-conventions.yaml
phalanx context list
```

**3. HTTP API directly.** `POST /api/contexts` with `{name, content, docType, tags}` (camelCase request, snake_case response). To bind to an agent, include the new context's id in the `contextIds` array of `POST /api/agents` or `PUT /api/agents/:id`.

The orchestrator eager-loads `context_documents` for each agent and the runtime concatenates them to the system prompt under a clearly-delimited header:

```
## NON-NEGOTIABLE: Acme coding conventions
<content>
```

---

## File-pattern scoping

The `agent.config.filePatterns` field gates whether an agent runs against a given PR. Patterns use a simple glob:

| Pattern | Matches |
|---|---|
| `*.go` | `main.go` (bare filenames), not `cmd/main.go` |
| `**/*.go` | any `.go` file at any depth |
| `**/*.tsx` | `dashboard/src/pages/Sessions.tsx` |
| `src/**` | any path under `src/` |
| `cmd/server/main.go` | exact match |
| `?` | single non-slash character |

The match is run against each changed file's path. If `skipIfNoMatch` is `true` and no file matches, the agent emits a `not_applicable` report without calling the LLM and the orchestrator records a `agent.skipped` audit event. If `skipIfNoMatch` is `false`, the agent runs regardless of file paths (useful for project-wide rules like "the changelog must be updated").

Where this lives in code: `internal/agent/runtime.go:matchGlob` — unit-tested in `runtime_test.go`.

---

## Temperature, max tokens, fallback models

### Temperature

Default is `0.0` and you should almost always keep it there. Review agents should give the same verdict on the same diff every time they see it; non-zero temperature reintroduces randomness and makes the audit trail less useful.

The only case for non-zero temperature is experimental skills where you actively want variation (e.g. "suggest three different refactors").

### Max tokens

The response ceiling for the LLM call. Default 4096. A checklist + a few findings rarely needs more than 2000 tokens; bump to 8192 for skills that emit long explanations (documentation, architecture).

Phalanx does not chunk or summarise the diff today — the full PR diff is passed in one request. For very large PRs (tens of thousands of lines) you'll hit the model's *input* context window before the output ceiling ever matters.

### Fallback models

Two levels of retry/fallback happen:

1. **Provider-level retries** — configured in `provider.config.maxRetries` and `retryDelayMs`. The router retries the same provider/model on transient errors with exponential backoff.
2. **Fallback provider** — if the agent config sets `fallbackProviderId`, a failure after all retries jumps to that provider. Common pattern: primary = `openai` / `gpt-4.1`, fallback = `deepseek` / `deepseek-v3` for cost control during OpenAI outages.

Every retry and fallback emits an audit event (`llm.error`, `llm.fallback`), so you can see exactly what happened in `/api/audit` or the dashboard.

---

## API tokens

Phalanx ships with a built-in bearer-token check enforced by middleware on every `/api/*` route. Webhooks (`/api/webhooks/*`) and `/health` bypass it — webhooks are authenticated by signature instead (see below).

Set the accepted tokens as a comma-separated list:

```bash
PHALANX_API_TOKENS=phx_prod_eng_2026_xxx,phx_ci_2026_yyy
```

- **Empty disables auth entirely.** This is the default for local dev so `make run` keeps working out of the box. **Production deployments MUST set this** — the server logs a warning at startup when it isn't.
- Tokens are matched verbatim (constant-time compare) against the `Authorization: Bearer <token>` header. Use opaque, high-entropy values; rotate by appending the new token, redeploying clients, and removing the old one.
- The CLI (`phalanx --token ...` / `PHALANX_TOKEN`), dashboard (set the token in the **Settings** page; persisted to `localStorage`), and GitHub Action (`with: phalanx_token: ...`) already send the header.

For per-user audit trails, terminate auth in your reverse proxy and forward the user identity as the `engineerId` field on `POST /api/decisions`. The bearer-token check is platform-wide; user identity for decisions is captured separately.

### Webhook signatures

When `GITHUB_WEBHOOK_SECRET` is set, every `/api/webhooks/github` request must carry a valid `X-Hub-Signature-256: sha256=<hex(hmac_sha256(secret, body))>` header — the standard GitHub format. Configure the same string in the repo/org webhook **Secret** field.

When `GITLAB_WEBHOOK_SECRET` is set, every `/api/webhooks/gitlab` request must carry an `X-Gitlab-Token` header equal to the secret. Configure the same string in the project webhook **Secret token** field.

Empty secrets disable verification (so you can run unauthenticated locally), but production MUST set them — without them, anyone who can reach the webhook URL can spawn reviews.

---

## Next steps

- [Usage](usage.md) — how to actually trigger reviews and read the results once your providers/skills/agents are set up.
- [Dashboard](dashboard.md) — visual walkthrough of the same concepts.
- [Development → Adding a skill](development.md#adding-a-built-in-skill) — deeper dive on the prompt contract and the response parser.
