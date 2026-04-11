# GitHub deployment

Wire Phalanx into GitHub pull requests. Three integration styles — pick the one that matches how your team already runs CI:

- [Option A: GitHub Action (recommended)](#option-a-github-action-recommended) — workflow runs on each PR, polls the server, blocks merge on failure.
- [Option B: Webhook (server-driven)](#option-b-webhook-server-driven) — GitHub posts to Phalanx directly; no CI job needed.
- [Option C: Manual via CLI or API](#option-c-manual-via-cli-or-api) — operators trigger reviews out-of-band.

All three depend on the same prerequisites: a reachable Phalanx server and a GitHub token that can read diffs and post comments.

---

## Prerequisites

1. **A running Phalanx server** reachable from GitHub.com (or GitHub Enterprise). For Options A and C the server needs an inbound endpoint on the public internet; for Option B GitHub needs to reach the server directly, so you must expose the `/api/webhooks/github` path publicly (use a reverse proxy, Cloudflare Tunnel, Tailscale Funnel, ngrok, etc.).
2. **A GitHub App or fine-grained personal access token** with:
   - `pull_requests: read & write` — post comments and check runs
   - `contents: read` — fetch PR diffs via `/compare`
   - `checks: write` — create check runs
3. At least one LLM provider and one enabled agent configured in Phalanx — see [Configuration](configuration.md).

Set the token on the Phalanx server so it can fetch diffs and post back:

```bash
# .env or docker-compose environment
GITHUB_TOKEN=ghp_xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx
GITHUB_API_URL=https://api.github.com            # or your GHE URL
GITHUB_WEBHOOK_SECRET=<shared secret>            # only needed for Option B
```

---

## Option A: GitHub Action (recommended)

The ready-to-use composite action lives at `integrations/github-action/action.yml`. Your team's workflow calls it once per PR and the action handles triggering, polling, and failing the check.

### Step 1 — Store Phalanx credentials as repository secrets

In **Settings → Secrets and variables → Actions** of the repository (or the org):

| Secret | Value |
|---|---|
| `PHALANX_URL` | The public base URL of your Phalanx server, e.g. `https://phalanx.example.com` |
| `PHALANX_TOKEN` | An API token the server recognises (see [Configuration → API tokens](configuration.md#api-tokens-optional)) |

### Step 2 — Add a workflow

Create `.github/workflows/phalanx.yml` in the repo you want reviewed:

```yaml
name: Phalanx Review

on:
  pull_request:
    types: [opened, synchronize, reopened, ready_for_review]

jobs:
  review:
    runs-on: ubuntu-latest
    permissions:
      pull-requests: write
      checks: write
      contents: read
    steps:
      - uses: phalanx-ai/phalanx/integrations/github-action@main
        with:
          phalanx_url: ${{ secrets.PHALANX_URL }}
          phalanx_token: ${{ secrets.PHALANX_TOKEN }}
          fail_on: fail              # fail | warn | none
          timeout: 300000            # ms (5 minutes)
          # Optional: run a subset of agents
          # agents: "security,accessibility,complexity"
```

Pin to a released tag (`@v1`) rather than `@main` for production.

### Step 3 — Protect the branch

In **Settings → Branches → Branch protection rules** for `main`:
- Require status checks to pass before merging
- Add `review / review` (the job name) as a required check

Now every PR opened or pushed runs the action. The action:
1. Reads the PR head/base SHA from the `pull_request` event.
2. `POST /api/reviews` to the Phalanx server with `triggerSource: "ci-action"`.
3. Polls `GET /api/reviews/:id` every 3 s, logging `progress.completed/total`.
4. On `status=completed`, writes the composite Markdown to `phalanx-report.md` (uploaded as a workflow artifact).
5. Fails the step (and the whole check) if the verdict matches `fail_on`.

### Action inputs

| Input | Default | Description |
|---|---|---|
| `phalanx_url` | — | **required** — Base URL of the Phalanx server |
| `phalanx_token` | — | **required** — API token |
| `agents` | `""` | Comma-separated list of skill slugs to run; empty = all enabled |
| `fail_on` | `fail` | `fail` (blocks on verdict=fail), `warn` (blocks on warn+fail), `none` (never blocks) |
| `timeout` | `120000` | Milliseconds to wait for completion |
| `post_comment` | `true` | Whether the server should also post a PR comment (the action always uploads the report as an artifact) |

### Action outputs

Expose these via `steps.<id>.outputs.*`:

| Output | Description |
|---|---|
| `session_id` | Phalanx review session UUID |
| `overall_verdict` | `pass` / `warn` / `fail` / `error` |
| `report_url` | Link to the session in the dashboard (`${phalanx_url}/sessions/<id>`) |

Example — comment the session link on the PR:

```yaml
- name: Phalanx
  id: phalanx
  uses: phalanx-ai/phalanx/integrations/github-action@v1
  with:
    phalanx_url: ${{ secrets.PHALANX_URL }}
    phalanx_token: ${{ secrets.PHALANX_TOKEN }}

- name: Post session link
  if: always()
  uses: peter-evans/create-or-update-comment@v4
  with:
    issue-number: ${{ github.event.pull_request.number }}
    body: |
      🛡️ Phalanx review: ${{ steps.phalanx.outputs.overall_verdict }}
      → ${{ steps.phalanx.outputs.report_url }}
```

---

## Option B: Webhook (server-driven)

If you don't want every PR to spawn a GitHub Actions runner, let GitHub POST directly to Phalanx and have the server fetch the diff and post results back itself. This is a lower-latency setup but requires the server to be reachable from GitHub's IPs.

### Step 1 — Expose `/api/webhooks/github`

Whatever reverse proxy fronts your Phalanx server should route `/api/webhooks/github` to the server without stripping the body or rewriting the path. Nginx example:

```nginx
location /api/webhooks/github {
    proxy_pass http://phalanx:3100/api/webhooks/github;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_read_timeout 30s;
}
```

### Step 2 — Generate a webhook secret

```bash
openssl rand -hex 32
```

Put it in the server's environment as `GITHUB_WEBHOOK_SECRET` and restart. Today the handler does **not** verify the signature — this is a known gap; treat the webhook endpoint as untrusted and gate it at the proxy with an IP allowlist until signature verification is wired up.

### Step 3 — Configure the webhook in GitHub

In the repo's **Settings → Webhooks → Add webhook**:

| Field | Value |
|---|---|
| Payload URL | `https://phalanx.example.com/api/webhooks/github` |
| Content type | `application/json` |
| Secret | the value you set in `GITHUB_WEBHOOK_SECRET` |
| Which events? | *Let me select individual events* → **Pull requests** |
| Active | ☑ |

Click **Add webhook**. GitHub sends a ping — the server replies `200` and the webhook shows a green check.

From now on, every `pull_request` event (`opened`, `synchronize`, `reopened`, `ready_for_review` — drafts are ignored) creates a `review_sessions` row, enqueues a review task, and returns `202 Accepted` immediately.

### Step 4 — Verify

Push a commit to a PR branch. In the Phalanx logs:

```
POST /api/webhooks/github HTTP/1.1 ... 202
session.created  session.queued  session.running  agent.started  ... session.completed
```

And in the repo's PR, a comment from the bot user owning `GITHUB_TOKEN` plus a `Phalanx Review` check run.

---

## Option C: Manual via CLI or API

Handy for backfilling reviews on historical PRs or running ad-hoc reviews during development.

### CLI

The `phalanx review` command auto-detects the PR context from environment variables (`GITHUB_REPOSITORY`, `GITHUB_SHA`) and from git (`origin/main` merge base):

```bash
./bin/phalanx review \
  --server https://phalanx.example.com \
  --token $PHALANX_TOKEN \
  --repo acme/widget \
  --pr 42 \
  --wait \
  --output report.md
```

`--wait` polls until the review completes; `--output` saves the composite Markdown.

### Raw API

See the [full Usage guide](usage.md#triggering-reviews) for the request shape. Short form:

```bash
curl -sS -X POST https://phalanx.example.com/api/reviews \
  -H "Authorization: Bearer $PHALANX_TOKEN" \
  -H 'Content-Type: application/json' \
  -d '{
    "platform": "github",
    "repository": "acme/widget",
    "prNumber": 42,
    "headSha": "<head sha>",
    "baseSha": "<base sha>",
    "triggerSource": "api"
  }'
```

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| Action step fails with `Failed to trigger review (HTTP 000)` | The runner can't reach `PHALANX_URL` | Verify the URL is reachable from GitHub-hosted runners; self-hosted runners with restricted egress need firewall rules |
| Action hangs and times out | Server received the trigger but the worker never picked it up | Check Redis is running and the server logs `Review queue worker started` at boot |
| Comment never appears on PR | `GITHUB_TOKEN` on the server doesn't have `pull_requests: write` | Regenerate the token with the right scope; re-check `docker compose logs phalanx` for `github 403` |
| Diff fetch fails with 404 | Base SHA no longer exists (force-pushed) | Manual rebase the PR branch, or re-trigger after GitHub accepts the push |
| Webhook deliveries show 500 in GitHub UI | Malformed payload or server error | Click the delivery in **Settings → Webhooks → Recent Deliveries** to see the exact request and response bodies |
