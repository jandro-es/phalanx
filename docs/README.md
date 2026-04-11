# 📚 Phalanx Documentation

Operational and development documentation for the Phalanx multi-agent pull-request review platform.

## Pick your path

| I want to… | Read |
|---|---|
| Run Phalanx on my laptop | [Local deployment](deployment-local.md) |
| Wire it into GitHub PRs | [GitHub deployment](deployment-github.md) |
| Wire it into GitLab MRs | [GitLab deployment](deployment-gitlab.md) |
| Add an LLM provider, skill, or agent | [Configuration](configuration.md) |
| Trigger reviews, read reports, make decisions | [Usage](usage.md) |
| Navigate the web dashboard | [Dashboard](dashboard.md) |
| Hack on the code | [Development](development.md) |

## What Phalanx is

A Go server that runs specialised AI agents against every pull request — one agent per quality dimension (security, accessibility, performance, …) — and posts a composite Markdown report back to GitHub/GitLab with a full tamper-evident audit trail.

The moving parts:

```
GitHub/GitLab webhook  ─────┐
CLI / API caller       ─────┼──►  chi HTTP server  ──►  asynq Redis queue
                            │                                   │
                            │                                   ▼
                            │                          Orchestrator (worker)
                            │                                   │
                            │                      ┌────────────┼────────────┐
                            │                      ▼            ▼            ▼
                            │               Agent runtime ─► LLM router ──► Anthropic / OpenAI / DeepSeek / self-hosted
                            │                      │
                            │                      ▼
                            │               Postgres (sessions, reports, audit)
                            │                      │
                            │                      ▼
                            └─────────►  Composite report ──► PR comment + check run
```

Three persistent data stores:
- **Postgres 16** — sessions, per-agent reports, decisions, audit log, and the configuration tables (skills, agents, providers).
- **Redis 7** — asynq task queue for review jobs. Reviews never run on the webhook request path.
- **Optional Vault** — for `vault://` provider key references.

Three operator entry points:
- **REST API** at `:3100` — what webhooks, the CLI, the dashboard, and CI integrations all talk to.
- **`phalanx` CLI** — wraps the API for terminal operators.
- **Web dashboard** at `:3000` (optional Docker profile) — React 19 app for browsing sessions, agents, providers, and the audit trail.

## Version & release info

- Go 1.23+ for the server and CLI. Everything compiles to static binaries.
- Node 22 + Vite 6 + React 19 + Tailwind 4 for the dashboard.
- Postgres 16, Redis 7, both pinned in `deploy/docker-compose.yml`.

## Conventions used in these docs

- Shell commands assume macOS/Linux and a POSIX shell.
- `curl` examples target `http://localhost:3100` — point them at your server if you've deployed it elsewhere.
- JSON request bodies use **camelCase** (what POST endpoints expect). JSON response bodies use **snake_case** (matching DB column names).
- Commands prefixed `docker exec deploy-postgres-1 psql …` work against the Compose stack. Replace the container name if you renamed the project.
