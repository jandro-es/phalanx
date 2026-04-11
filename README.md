# 🛡️ Phalanx

**AI-powered multi-agent pull request review platform** — written in Go.

Phalanx runs specialised AI agents against every pull request — each evaluating a single quality dimension (security, accessibility, performance, test coverage, etc.) — and delivers a structured Markdown report with checklists for a human engineer to make the final merge decision. Every review, every LLM call, and every approval is immutably audited.

---

## Key Features

- **One agent per criterion** — modular, auditable, independently tunable
- **Model-agnostic** — Anthropic, OpenAI, DeepSeek, Google, or self-hosted (vLLM, Ollama)
- **Pipeline-native** — GitHub Actions and GitLab CI first-class integrations
- **Audit-grade** — append-only log with optional hash-chain integrity verification
- **10 built-in agents** — accessibility, security, complexity, architecture, test coverage, API contract, performance, documentation, error handling, code style
- **Zero-code extensibility** — add a new review criterion with a YAML file
- **Single binary** — compiles to a ~20MB static binary, deploys anywhere
- **Low resource footprint** — Go's goroutine-based concurrency handles hundreds of parallel agent executions with minimal memory

---

## Quick Start

### Docker Compose (fastest)

```bash
cp .env.example .env
# Edit .env with your API keys

docker compose -f deploy/docker-compose.yml up -d
```

### Build from source

```bash
# Prerequisites: Go 1.23+, PostgreSQL 16, Redis 7
make build
make migrate
make seed
make run
```

### Binary distribution

```bash
# Download the latest release
curl -L https://github.com/phalanx-ai/phalanx/releases/latest/download/phalanx-linux-amd64.tar.gz | tar xz

# Run the server
./phalanx-server

# Use the CLI
./phalanx review --repo owner/name --pr 42 --server http://localhost:3100
```

---

## GitHub Integration

Add to `.github/workflows/phalanx.yml`:

```yaml
name: Phalanx Review
on:
  pull_request:
    types: [opened, synchronize, ready_for_review]

jobs:
  review:
    runs-on: ubuntu-latest
    permissions:
      pull-requests: write
      checks: write
      contents: read
    steps:
      - uses: phalanx-ai/action@v1
        with:
          phalanx_url: ${{ secrets.PHALANX_URL }}
          phalanx_token: ${{ secrets.PHALANX_TOKEN }}
          fail_on: fail
```

## GitLab Integration

```yaml
include:
  - remote: 'https://raw.githubusercontent.com/phalanx-ai/phalanx/main/integrations/gitlab-template/.phalanx.gitlab-ci.yml'
```

---

## Adding a Custom Agent

1. Write a skill YAML:

```yaml
slug: my-custom-check
name: My Custom Check
version: 1
system_prompt: |
  You are reviewing code for [your criteria]...
checklist_template: |
  ## My Custom Check
  **Verdict:** {{verdict}}
  ### Checklist
  - [{{item_1}}] First check
  ### Findings
  {{findings}}
```

2. Register and bind:

```bash
phalanx skill register skills/my-custom-check.yaml --server http://localhost:3100
phalanx agent create --skill my-custom-check --provider anthropic --server http://localhost:3100
```

---

## Architecture

```
GitHub/GitLab webhook
       │
       ▼
   chi Router ──► Orchestrator ──► goroutine pool (bounded)
                       │              ├── Accessibility Agent
                       │              ├── Security Agent
                       │              └── ... (N agents)
                       │
                       ├── LLM Router ──► Anthropic / OpenAI / DeepSeek / Self-hosted
                       │
                       ▼
                 Report Builder ──► PR Comment + Check Run
                       │
                       ▼
                 Audit Logger (append-only pgx)
```

## Project Structure

```
phalanx/
├── cmd/
│   ├── server/        # HTTP server entry point
│   └── cli/           # CLI tool entry point
├── internal/
│   ├── types/         # All domain types
│   ├── audit/         # Append-only audit logger with hash chain
│   ├── llm/           # LLM router + rate limiter
│   │   └── adapters/  # Anthropic + OpenAI-compatible adapters
│   ├── agent/         # Single-agent execution runtime
│   ├── orchestrator/  # Parallel fan-out, result collection
│   ├── report/        # Composite Markdown report builder
│   ├── platform/      # GitHub + GitLab API clients
│   ├── api/           # HTTP handlers (chi)
│   ├── config/        # Configuration loader
│   └── secrets/       # Vault / env / file secret resolver
├── skills/            # 10 built-in skill definitions (YAML)
├── config/            # Provider configuration
├── migrations/        # PostgreSQL schema
├── integrations/      # GitHub Action + GitLab CI template
├── deploy/            # Dockerfile, docker-compose, Helm chart
├── dashboard/         # React web UI (separate build)
└── docs/              # Comprehensive plan document
```

## API

| Endpoint | Method | Description |
|---|---|---|
| `/api/webhooks/github` | POST | GitHub webhook |
| `/api/webhooks/gitlab` | POST | GitLab webhook |
| `/api/reviews` | POST | Trigger review |
| `/api/reviews/:id` | GET | Session status + reports |
| `/api/decisions/:sessionId` | POST | Submit approval |
| `/api/agents` | GET/POST | List/create agents |
| `/api/skills` | GET/POST | List/create skills |
| `/api/providers` | GET/POST | List/create providers |
| `/api/audit` | GET | Query audit log |
| `/api/audit/session/:id` | GET | Session audit trail |
| `/api/audit/verify` | GET | Hash chain verification |
| `/api/audit/export` | GET | Export as JSON-lines |
| `/health` | GET | Health check |

---

## Why Go?

- **Single static binary** — no runtime dependencies, no node_modules, no Python virtualenvs
- **Goroutine concurrency** — fan out 10+ agents in parallel with bounded resource usage
- **~20MB Docker image** — distroless base, fast cold starts
- **Memory efficient** — handles hundreds of concurrent reviews with ~50MB RSS
- **Fast startup** — server ready in <100ms vs seconds for Node.js

## License

MIT
