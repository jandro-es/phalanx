# Dashboard

The web dashboard is a React 19 + Vite 6 + Tailwind 4 SPA under `dashboard/`. It's a thin read-write wrapper around the Phalanx REST API — everything you can do in the UI you can also do with `curl` against the same endpoints (see [Usage](usage.md)).

- [Starting the dashboard](#starting-the-dashboard)
- [Navigation](#navigation)
- [Reviews page](#reviews-page)
- [Session detail page](#session-detail-page)
- [Audit trail page](#audit-trail-page)
- [Agents page](#agents-page)
- [Providers page](#providers-page)
- [Configuring the API URL](#configuring-the-api-url)
- [Local development](#local-development-of-the-dashboard)

---

## Starting the dashboard

### Under Docker Compose

The dashboard is gated behind the `dashboard` Compose profile, so a plain `docker compose up -d` won't start it. Use:

```bash
docker compose -f deploy/docker-compose.yml --profile dashboard up -d
```

Then open **<http://localhost:3000>**.

The container builds the Vite app with `npm run build` at image-build time and serves the static `dist/` with `serve -s` — so client-side routes (`/sessions/:id`, `/audit`, …) all fall back to `index.html` correctly on refresh.

### Standalone (for dev)

```bash
cd dashboard
npm install
npm run dev
```

Vite serves on port 3000 with hot-reload enabled. See [Local development](#local-development-of-the-dashboard) below.

### What you need before opening it

- Phalanx server reachable at `http://localhost:3100` (or wherever `VITE_PHALANX_API_URL` points).
- At least one **skill**, one **provider**, and one enabled **agent** — otherwise the Reviews and Agents pages will show empty states. The [Local deployment](deployment-local.md#seed-the-built-in-skills-and-providers) guide covers seeding.

---

## Navigation

The top nav has four tabs plus the Phalanx logo (returns to Reviews):

| Tab | Route | Purpose |
|---|---|---|
| 🛡️ Phalanx / Reviews | `/` | List of review sessions, newest first |
| Audit Trail | `/audit` | Event-level audit log with filters |
| Agents | `/agents` | Configured review agents |
| Providers | `/providers` | Registered LLM providers |

Session detail pages are reached via `/sessions/:id` — there's no nav link, you click into a row on the Reviews page.

---

## Reviews page

**Route:** `/`

A paginated table of `review_sessions`, newest first. The server-side query is `GET /api/reviews?limit=50`.

Columns:

| Column | Source |
|---|---|
| **PR** | `#prNumber` with `pr_title` underneath — links to `/sessions/:id` |
| **Repository** | `repository_full_name` |
| **Author** | `pr_author` |
| **Status** | `status` pill (`queued`, `running`, `completed`, `failed`, `cancelled`) |
| **Verdict** | `overall_verdict` pill — only present when `status == completed` |
| **Time** | `started_at` as local-time |

### Status and verdict colours

| Pill | Meaning |
|---|---|
| 🔵 `queued` / `running` | In flight. Click the row to see live per-agent progress. |
| 🟢 `completed` | Orchestrator finished successfully. Verdict reflects the aggregated result. |
| 🔴 `failed` | Orchestrator aborted before finishing. Check the audit trail for the cause (usually `llm.error` or diff-fetch failure). |
| ⚪ `cancelled` | Operator-cancelled (reserved — not emitted today). |

Verdict aggregation rule (`internal/report/builder.go:computeOverall`):
- Any `fail` or `error` → `fail`
- Else any `warn` → `warn`
- Else → `pass`

### Empty state

If there are no sessions yet (e.g. freshly-seeded server), the page shows:

> No review sessions yet.

with an empty table. That's the expected first-use state.

### What it won't do

- No filtering in the UI today — to narrow by repo, author, status, use the API directly or add a query string hack. Filtering is tracked as future work.
- No pagination controls in the UI — the table always renders the first 50. Paginate via the API.
- No auto-refresh — refresh the page to see new sessions.

---

## Session detail page

**Route:** `/sessions/:id`

Three sections:

### 1. Header card

Shows the PR title, repo, author, branches, session ID, and the short commit SHA. On the right is the overall verdict pill and — when the session is `completed` and has no decision yet — a **Make Decision** button.

For sessions in `running` state, a progress bar shows `progress.completed / progress.total` (the number of per-agent reports written so far over the total number of enabled agents). The component polls `GET /api/reviews/:id` every 3 seconds until the status becomes `completed` or `failed`, so you can leave the tab open and watch the bar fill up.

### 2. Decisions panel

Appears only once at least one decision has been recorded. Each entry shows:
- Decision icon (✅ approve, 🔄 request_changes, ⏸️ defer)
- Engineer name + decision verb
- Timestamp
- Justification (if provided)
- Count of overridden verdicts (if any)

### 3. Agent reports

One collapsible `<details>` per agent, with the agent skill name, verdict badge, model used, latency, token counts, and estimated cost in the summary line. Expand to see the full Markdown report rendered as preformatted text.

The reports come from `GET /api/reviews/:id → .reports`, sorted by `created_at`. The skill slug is mapped to a display name by a simple `kebab-case → Title Case` helper; emojis come from a hardcoded map in `internal/report/builder.go`.

### Making a decision

Click **Make Decision**. A form opens with:

- Three buttons (approve / request changes / defer) that set the decision type.
- A textarea for an optional justification (required if you override any verdicts).
- **Submit Decision** / **Cancel**.

When you submit, the dashboard:
1. `POST /api/decisions/:sessionId` with the decision body.
2. Re-fetches `GET /api/reviews/:id` so the Decisions panel updates immediately.
3. Hides the form.

### Known limitations

- **No in-UI verdict override flow.** If you want to approve-with-override you need to hit the API directly (see [Usage → Overriding a failing verdict](usage.md#overriding-a-failing-verdict)).
- **Engineer identity is stubbed** as `"dashboard-user"`. A real deployment should either wire the dashboard to your SSO and forward the subject claim, or proxy-inject a header the dashboard reads.
- **No diff viewer.** The dashboard doesn't show the PR diff itself — you're expected to open the PR on GitHub/GitLab for the raw changes. The reports are the thing the dashboard is for.

---

## Audit trail page

**Route:** `/audit`

A paginated, filterable view of `audit_log`. Calls `GET /api/audit?limit=200` plus filter params.

### Filters

Three filter inputs + a Search button:

| Filter | Matches |
|---|---|
| **Session ID** | Exact match on `session_id` |
| **Event Type** | Pick from the dropdown (`session.created`, `session.completed`, `agent.completed`, `llm.request`, `llm.response`, `decision.approve`, `decision.request_changes`, `config.agent.created`, `config.skill.updated`) |
| **Actor** | Exact match on `actor` (e.g. `system`, `api`, `alice@acme.com`) |

Click **Search** to re-query. Filters are not URL-bound today — reloading the page resets them.

### Results table

| Column | Content |
|---|---|
| ID | Sequential `audit_log.id` |
| Time | `created_at` (local) |
| Event | Event type pill, colour-coded by prefix (session.*, agent.*, llm.*, report.*, decision.*, config.*) |
| Actor | `actor` column |
| Session | First 8 chars of session UUID, links to `/sessions/:id` |
| Payload | First 60 chars of the JSON payload as a `<details>` summary; click to expand the full JSON |

Footer: **`N entries shown`** — how many rows came back for the current filter.

### Common queries

- **What happened on this session?** Paste the session UUID into the Session ID filter.
- **Which LLM calls failed today?** Event Type = `llm.error`.
- **Who approved reviews this week?** Event Type = `decision.approve`. The payload contains the engineer name and justification.
- **Who touched the config?** Event Type = `config.agent.updated` (etc.). Actor will be `api` for HTTP-triggered changes.

### Hash chain verification

Not in the UI today — use the CLI or API (`GET /api/audit/verify`). A future enhancement is a small banner on this page showing live chain validity.

---

## Agents page

**Route:** `/agents`

A table of configured agents with their skill, provider, model, temperature, priority, and enabled/disabled toggle.

| Column | Field |
|---|---|
| Agent | `name` |
| Skill | `skill_slug` (pill) |
| Provider / Model | `provider_name / model_override || "default"` |
| Temperature | `temperature` |
| Priority | `priority` |
| Status | Button — `Enabled` (green) or `Disabled` (grey). Click to toggle. |

Clicking the toggle calls `POST /api/agents/:id` with `{enabled: false}` (the UI currently only supports disabling from here; re-enabling an agent needs a PUT or direct DB edit — tracked).

### Empty state

```
No agents configured yet. Use `phalanx agent` or POST /api/agents to create one.
```

Agent creation via the dashboard is not wired up yet — use the [CLI / API](configuration.md#creating-an-agent) to create agents, and the dashboard to monitor and disable them.

---

## Providers page

**Route:** `/providers`

A card grid of registered LLM providers. Each card shows:

- **Name** + base URL
- **Default Model** (monospace)
- **Auth Method** (`bearer`, `api-key-header`, `none`)
- **Available Models** — chip list from the `models` array
- **Configuration** — collapsed `<details>` with the JSONB config blob (rate limits, retry settings, timeout)

Providers are read-only in the dashboard today — use the [CLI / API](configuration.md#registering-providers) to register or update them.

### Empty state

```
No LLM providers configured. Register one with POST /api/providers or via the CLI.
```

This state blocks reviews from doing anything useful — without a provider, no agent can run.

---

## Configuring the API URL

By default the dashboard talks to `http://localhost:3100`. Override this at **build time** for the Docker image, or at **dev-server startup** when running `npm run dev`:

```bash
# build-time (Docker)
docker build -f deploy/Dockerfile.dashboard \
  --build-arg VITE_PHALANX_API_URL=https://phalanx.example.com \
  -t phalanx-dashboard:prod .

# dev server
cd dashboard
VITE_PHALANX_API_URL=http://192.168.1.20:3100 npm run dev
```

The variable name **must** start with `VITE_` — Vite only exposes env vars with that prefix to the client bundle. It ends up in `App.tsx` as `import.meta.env.VITE_PHALANX_API_URL`.

You can also override at runtime (after the bundle is already built) by storing a token in `localStorage`:

```javascript
localStorage.setItem("phalanx_token", "bearer-token-here")
```

The `useApi()` hook reads this and injects `Authorization: Bearer <token>` into every request. Useful when you're behind a proxy that expects a token and you're not rebuilding the dashboard image for each environment.

---

## Local development of the dashboard

```bash
cd dashboard
npm install
npm run dev
```

Vite serves on <http://localhost:3000> with hot-reload enabled. The React pages re-render on file save.

File structure:

```
dashboard/
├── index.html                 # Vite entry
├── package.json               # React 19, Vite 6, Tailwind 4, @tailwindcss/vite
├── vite.config.ts             # @vitejs/plugin-react + @tailwindcss/vite
├── tsconfig.json
├── tsconfig.node.json
└── src/
    ├── main.tsx              # React root — createRoot(document.getElementById("root"))
    ├── index.css             # @import "tailwindcss";  (Tailwind v4 style)
    ├── vite-env.d.ts         # Typings for import.meta.env
    ├── App.tsx               # BrowserRouter + nav + route table + useApi() hook
    └── pages/
        ├── Sessions.tsx
        ├── SessionDetail.tsx
        ├── AuditTrail.tsx
        ├── AgentConfig.tsx
        └── ProviderConfig.tsx
```

### Adding a new page

1. Create `src/pages/MyPage.tsx`, export a named function component.
2. Add it to the route table in `App.tsx`:
   ```tsx
   <Route path="/my-page" element={<MyPage />} />
   ```
3. Add a `<NavLink>` in the nav bar (same file, just above the `<Routes>`).
4. Use the `useApi()` hook for server calls — it already handles token injection and error shapes.

### The `useApi()` hook

Defined in `App.tsx`:

```ts
const api = useApi();
const data = await api.get("/api/skills");
const created = await api.post("/api/agents", { name: "..." });
```

Both methods `throw new Error(...)` on non-2xx responses — wrap in `try/catch` or `.catch()` and set an error state so the page doesn't silently blank out. Every page in `src/pages/` follows this pattern; copy one when you add another.

### Styling

Tailwind v4 is configured via `@tailwindcss/vite` — there's no `tailwind.config.js`. The CSS entry is `src/index.css` with the single directive `@import "tailwindcss";`, and Tailwind auto-scans your TSX for class names.

Colour conventions used across the pages:
- Success: `green-100` background / `green-700` text (pass, enabled, ok)
- Warning: `yellow-100` / `yellow-800` (warn)
- Error: `red-100` / `red-700` (fail, error, destructive actions)
- Info: `blue-100` / `blue-700` (running, links, info)
- Muted: `gray-50` / `gray-600` (backgrounds, labels)

Stick to those so new pages look like they belong.
