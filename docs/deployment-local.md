# Local deployment

Run the entire Phalanx stack on your laptop. Two paths: **Docker Compose** (recommended — single command) and **from source** (faster edit-reload loop for development).

- [Option 1: Docker Compose](#option-1-docker-compose-recommended)
- [Option 2: From source](#option-2-from-source)
- [Seed the built-in skills and providers](#seed-the-built-in-skills-and-providers)
- [Create your first agent and trigger a review](#create-your-first-agent-and-trigger-a-review)
- [Health checks and troubleshooting](#health-checks-and-troubleshooting)

---

## Option 1: Docker Compose (recommended)

### Prerequisites

- Docker Desktop 4.x or a working `docker` + `docker compose` CLI on Linux.
- ~500 MB free disk for the Postgres volume.

### Step 1 — Clone and configure

```bash
git clone https://github.com/phalanx-ai/phalanx.git
cd phalanx
cp .env.example .env
```

Edit `.env` and set at least one LLM provider key:

```bash
ANTHROPIC_API_KEY=sk-ant-...
# or
OPENAI_API_KEY=sk-...
```

Leave everything else at the defaults — the Compose stack wires DB and Redis URLs automatically.

### Step 2 — Bring up the core stack

```bash
docker compose -f deploy/docker-compose.yml up -d
```

This starts three containers:
- `deploy-postgres-1` — Postgres 16, migrations auto-applied on first boot.
- `deploy-redis-1` — Redis 7 for the asynq review queue.
- `deploy-phalanx-1` — the Go server + worker.

Verify the server is healthy:

```bash
curl -s http://localhost:3100/health
# {"database":true,"status":"healthy"}
```

### Step 3 — (Optional) start the dashboard

```bash
docker compose -f deploy/docker-compose.yml --profile dashboard up -d
```

Then open **<http://localhost:3000>**.

The dashboard is gated behind the `dashboard` Compose profile because it's optional; plain `up -d` won't start it. Its browser code talks to `http://localhost:3100` directly, so the API port must be reachable from your host.

### Step 4 — Tear down

```bash
docker compose -f deploy/docker-compose.yml down       # stop containers, keep volumes
docker compose -f deploy/docker-compose.yml down -v    # also wipe Postgres + Redis volumes
```

---

## Option 2: From source

Useful for iterating on Go code. The server binary will connect to your own Postgres and Redis.

### Prerequisites

- Go 1.23+
- Postgres 16 running on `localhost:5432`
- Redis 7 running on `localhost:6379`
- `psql` on `$PATH` for running the migration

Fastest way to get the datastores up without running the full compose stack:

```bash
docker compose -f deploy/docker-compose.yml up -d postgres redis
```

### Step 1 — Build binaries

```bash
make build
```

Produces `bin/phalanx-server` and `bin/phalanx` (the CLI).

### Step 2 — Apply the schema

If Postgres was started via Compose the migration was auto-applied by `docker-entrypoint-initdb.d`, so you can skip this. Otherwise:

```bash
export DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx
make migrate
```

### Step 3 — Run the server

```bash
export DATABASE_URL=postgresql://phalanx:phalanx@localhost:5432/phalanx
export REDIS_URL=redis://localhost:6379
export ANTHROPIC_API_KEY=sk-ant-...    # or OPENAI_API_KEY
make run
```

The server listens on `:3100`, registers any LLM providers it finds in the database with the router, starts the asynq worker in-process, and begins processing queued reviews.

### Step 4 — (Optional) run the dashboard with hot-reload

```bash
cd dashboard
npm install
npm run dev
```

Vite serves on <http://localhost:3000> with HMR. Set `VITE_PHALANX_API_URL` if your server is somewhere other than `http://localhost:3100`:

```bash
VITE_PHALANX_API_URL=http://127.0.0.1:3100 npm run dev
```

---

## Seed the built-in skills and providers

Skills and providers are rows in Postgres — a freshly booted server has **zero** of each. Seed them using the CLI:

```bash
# 1. Register all 10 built-in skills from skills/*.yaml
make seed

# 2. Register the default LLM providers from config/providers.yaml
#    (only providers whose env:// API key is actually set will work)
make seed-providers
```

Both targets are idempotent — they upsert on `(slug, version)` for skills and on `name` for providers, so you can re-run them any time without duplicating rows.

Verify:

```bash
curl -s http://localhost:3100/api/skills    | jq '.skills | length'     # 10
curl -s http://localhost:3100/api/providers | jq '.providers | length'  # 5 (or however many your providers.yaml defines)
```

---

## Create your first agent and trigger a review

A skill describes *what to review* and a provider describes *which LLM to call*. An **agent** binds them together. You need at least one agent enabled before a review will do anything.

### Create an agent via the API

```bash
# Look up the UUIDs of a skill and a provider
SKILL_ID=$(curl -s http://localhost:3100/api/skills    | jq -r '.skills[] | select(.slug=="security") | .id')
PROVIDER_ID=$(curl -s http://localhost:3100/api/providers | jq -r '.providers[] | select(.name=="anthropic") | .id')

curl -sS -X POST http://localhost:3100/api/agents \
  -H 'Content-Type: application/json' \
  -d "{
    \"name\": \"security-claude\",
    \"skillId\": \"$SKILL_ID\",
    \"providerId\": \"$PROVIDER_ID\",
    \"temperature\": 0.0,
    \"maxTokens\": 4096,
    \"enabled\": true,
    \"priority\": 10
  }"
```

See [Configuration → Agents](configuration.md#agents) for details on every field, including `modelOverride`, `filePatterns`, and `skipIfNoMatch`.

### Trigger a review

Against a live GitHub repo (the server will fetch the diff for you — requires `GITHUB_TOKEN`):

```bash
curl -sS -X POST http://localhost:3100/api/reviews \
  -H 'Content-Type: application/json' \
  -d '{
    "platform": "github",
    "repository": "acme/widget",
    "prNumber": 42,
    "headSha": "abc1234567890abcdef1234567890abcdef12345",
    "baseSha": "fedcba0987654321fedcba0987654321fedcba09",
    "triggerSource": "api"
  }'
# {"estimatedDurationMs":30000,"sessionId":"3fd59aa0-...","status":"queued"}
```

Or from the CLI with auto-detected git state:

```bash
./bin/phalanx review --server http://localhost:3100 \
  --repo acme/widget --pr 42 --wait
```

See [Usage → Triggering reviews](usage.md#triggering-reviews) for webhook, CI, and raw-diff flows.

---

## Health checks and troubleshooting

### Is the server alive?

```bash
curl -s http://localhost:3100/health
```

`{"database":true,"status":"healthy"}` means the HTTP server can reach Postgres. If `database` is `false` the server started but the DB is unreachable.

### Is the worker consuming tasks?

Enqueue a review, then look at the queue depth:

```bash
docker exec deploy-redis-1 redis-cli KEYS 'asynq:*' | head
docker exec deploy-redis-1 redis-cli LLEN 'asynq:{default}:pending'
```

If the pending count stays >0, the worker isn't running. Check `docker compose logs phalanx` for startup errors.

### Common problems

| Symptom | Likely cause | Fix |
|---|---|---|
| `Database ping failed` at boot | `DATABASE_URL` wrong or Postgres not up | `docker compose ps postgres` / check `$DATABASE_URL` |
| Agent reports all have `verdict=error` | LLM provider API key not set or wrong | `docker compose exec phalanx env \| grep API_KEY` |
| Review enqueues but never completes | Worker didn't start — check `Queue worker failed to start` in logs | Verify `REDIS_URL` reachable from inside the phalanx container |
| Dashboard loads but pages are empty | API is returning errors — open DevTools → Network | Usually means no seeded skills/providers/agents |
| `make seed` stops on duplicate key | You ran it against an older server without the idempotent upsert | Pull latest, rebuild the server, rerun |

Logs go to stdout in Compose:

```bash
docker compose -f deploy/docker-compose.yml logs -f phalanx
```

Structured zerolog output in dev mode shows the full audit trail of every enqueue → dequeue → LLM call → agent result → session complete.
