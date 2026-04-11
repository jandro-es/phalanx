# GitLab deployment

Wire Phalanx into GitLab merge requests. Two integration styles:

- [Option A: GitLab CI template (recommended)](#option-a-gitlab-ci-template-recommended) — a `.gitlab-ci.yml` job per MR, polls and blocks merge on failure.
- [Option B: Webhook (server-driven)](#option-b-webhook-server-driven) — GitLab posts MR events to Phalanx directly.

Both need the same prerequisites: a reachable Phalanx server and a GitLab token.

---

## Prerequisites

1. **A running Phalanx server** reachable from your GitLab instance (`gitlab.com` or self-managed). For Option A the server must be reachable from CI runners; for Option B GitLab needs to reach the server directly (via a public endpoint or private peering).
2. **A GitLab token** — either a [project access token](https://docs.gitlab.com/ee/user/project/settings/project_access_tokens.html) or a group/personal access token with:
   - `api` scope — fetch MR diffs and post notes.
3. At least one LLM provider and one enabled agent configured in Phalanx — see [Configuration](configuration.md).

Set the token on the Phalanx server:

```bash
# .env or docker-compose environment
GITLAB_TOKEN=glpat-xxxxxxxxxxxxxxxxxxxx
GITLAB_URL=https://gitlab.com                      # or https://gitlab.internal
GITLAB_WEBHOOK_SECRET=<shared secret>              # only needed for Option B
```

---

## Option A: GitLab CI template (recommended)

The template lives at `integrations/gitlab-template/.phalanx.gitlab-ci.yml` in this repo. It defines a `.phalanx-review` hidden job and a ready-to-use concrete job named `phalanx-review`.

### Step 1 — Define Phalanx CI/CD variables

In **Settings → CI/CD → Variables** on the project (or the parent group), add:

| Key | Value | Flags |
|---|---|---|
| `PHALANX_URL` | `https://phalanx.example.com` | Protect: ☑ |
| `PHALANX_TOKEN` | API token the server recognises | Masked: ☑, Protect: ☑ |

Optional variables:

| Key | Default | Description |
|---|---|---|
| `PHALANX_AGENTS` | empty (= all enabled) | Comma-separated skill slugs, e.g. `security,complexity` |
| `PHALANX_FAIL_ON` | `fail` | `fail` / `warn` / `none` |
| `PHALANX_TIMEOUT` | `120` | Seconds to wait for completion |

### Step 2 — Include the template

In the project's `.gitlab-ci.yml`:

```yaml
include:
  - remote: 'https://raw.githubusercontent.com/phalanx-ai/phalanx/main/integrations/gitlab-template/.phalanx.gitlab-ci.yml'

stages:
  - test

phalanx-review:
  extends: .phalanx-review
  stage: test
```

If you're hosting this repo on the same GitLab instance, prefer a `project:` include instead:

```yaml
include:
  - project: 'infra/phalanx'
    ref: main
    file: 'integrations/gitlab-template/.phalanx.gitlab-ci.yml'
```

### Step 3 — Restrict the job to MR pipelines

The template already ships with the right rule:

```yaml
rules:
  - if: '$CI_PIPELINE_SOURCE == "merge_request_event"'
```

So the job only runs on merge request pipelines, not on regular branch pipelines. Make sure your project has **Settings → Merge requests → Pipelines for merged results** enabled if you want it to run on the merged-result commit.

### Step 4 — Require the job before merge

**Settings → Merge requests → Merge checks** → enable *Pipelines must succeed*.

### What the job does

1. Reads `$CI_PROJECT_PATH`, `$CI_MERGE_REQUEST_IID`, `$CI_COMMIT_SHA`, and `$CI_MERGE_REQUEST_DIFF_BASE_SHA` from the GitLab CI environment.
2. `POST /api/reviews` to `$PHALANX_URL` with `triggerSource: "ci-action"`.
3. Polls `GET /api/reviews/:id` every 3 s until status is `completed` or `failed`.
4. Saves the composite Markdown to `phalanx-report.md` and uploads it as a CI artifact (retained 90 days, also registered as `codequality` so it shows in the MR overview).
5. Fails the job if the verdict matches `PHALANX_FAIL_ON`.

### Artifact access

After the job finishes, the composite report is downloadable from the job's artifacts panel. The full session (with agent-level detail and audit trail) lives in the Phalanx dashboard at `${PHALANX_URL}/sessions/<sessionId>`.

---

## Option B: Webhook (server-driven)

Skip CI entirely — GitLab posts MR events directly to Phalanx. Useful for mono-repos where you don't want an MR pipeline per push.

### Step 1 — Expose `/api/webhooks/gitlab`

Route `/api/webhooks/gitlab` through your reverse proxy. Same caveats as the GitHub webhook — today the handler does not verify the webhook secret, so treat the endpoint as untrusted and gate it at the proxy.

### Step 2 — Generate a webhook secret

```bash
openssl rand -hex 32
```

Set it as `GITLAB_WEBHOOK_SECRET` in the server environment.

### Step 3 — Configure the webhook

In the project's **Settings → Webhooks → Add new webhook**:

| Field | Value |
|---|---|
| URL | `https://phalanx.example.com/api/webhooks/gitlab` |
| Secret token | same as `GITLAB_WEBHOOK_SECRET` |
| Trigger | ☑ **Merge request events** |
| SSL verification | ☑ (unless using self-signed certs) |

Click **Add webhook**, then **Test → Merge request events**. Phalanx should respond `202` and you should see a new row in `review_sessions`.

### Step 4 — Verify

Open or update an MR. In the Phalanx logs:

```
POST /api/webhooks/gitlab HTTP/1.1 ... 202
session.created  session.queued  session.running  ...
```

And the server will post a note on the MR with the composite report once the review completes. (GitLab doesn't have a "check run" concept like GitHub, so the only signal on the MR page is the note.)

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| CI job logs `Failed to trigger review (HTTP 000)` | Runner can't reach `PHALANX_URL` | Either whitelist the host on shared runners, or use a runner inside your VPC |
| Job stays in `running` forever, then times out | Server received the trigger but the worker is idle | `docker compose logs phalanx` — ensure `Review queue worker started` is in the startup output |
| MR note never appears | `GITLAB_TOKEN` lacks `api` scope or is project-scoped to a different project | Regenerate with `api` scope on the right project/group |
| Compare API returns empty diff | `CI_MERGE_REQUEST_DIFF_BASE_SHA` isn't set (non-MR pipeline) | Ensure the job only runs on `merge_request_event` (the template already does) |
| Webhook deliveries show 404 | `GITLAB_URL` env var is the UI URL, not the API | The server appends `/api/v4` internally — set `GITLAB_URL=https://gitlab.com`, not `https://gitlab.com/api/v4` |
