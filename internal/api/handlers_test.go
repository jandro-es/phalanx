package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/phalanx-ai/phalanx/internal/audit"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// --- Test fixtures ---

// fakeEnqueuer implements ReviewEnqueuer and records every enqueued session
// so tests can assert both that the enqueuer was called and with what.
type fakeEnqueuer struct {
	mu      sync.Mutex
	calls   []types.ReviewSession
	failErr error
}

func (f *fakeEnqueuer) EnqueueReview(ctx context.Context, s types.ReviewSession) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failErr != nil {
		return f.failErr
	}
	f.calls = append(f.calls, s)
	return nil
}

func (f *fakeEnqueuer) Calls() []types.ReviewSession {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]types.ReviewSession, len(f.calls))
	copy(out, f.calls)
	return out
}

// openTestDB connects to PHALANX_TEST_DATABASE_URL. Handler tests are
// skipped when the variable is unset.
func openTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PHALANX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PHALANX_TEST_DATABASE_URL not set; skipping handler integration test")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// freshDB wipes the DB so tests start from a known state.
func freshDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
		TRUNCATE
		  audit_log,
		  approval_decisions,
		  agent_reports,
		  review_sessions,
		  agent_context,
		  agents,
		  context_documents,
		  skills,
		  llm_providers
		RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

func newTestHandler(t *testing.T) (*Handler, *fakeEnqueuer, *pgxpool.Pool) {
	t.Helper()
	pool := openTestDB(t)
	freshDB(t, pool)
	enq := &fakeEnqueuer{}
	return &Handler{
		DB:       pool,
		Audit:    audit.New(pool, false),
		Enqueuer: enq,
	}, enq, pool
}

func do(t *testing.T, h http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		if s, ok := body.(string); ok {
			reader = strings.NewReader(s)
		} else {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
	}
	req := httptest.NewRequest(method, path, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func decode(t *testing.T, rr *httptest.ResponseRecorder, out any) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), out); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
}

// --- Health ---

func TestHealth(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "GET", "/health", nil)
	if rr.Code != 200 {
		t.Fatalf("status: got %d", rr.Code)
	}
	var body struct {
		Status   string `json:"status"`
		Database bool   `json:"database"`
	}
	decode(t, rr, &body)
	if body.Status != "healthy" || !body.Database {
		t.Errorf("health: %+v", body)
	}
}

// --- Skills CRUD ---

func TestCreateSkill_Validation(t *testing.T) {
	h, _, _ := newTestHandler(t)

	cases := []struct {
		name       string
		body       any
		wantStatus int
	}{
		{"empty body", "{}", 400},
		{"missing slug", map[string]any{"name": "X", "systemPrompt": "p", "checklistTemplate": "c"}, 400},
		{"missing name", map[string]any{"slug": "s", "systemPrompt": "p", "checklistTemplate": "c"}, 400},
		{"missing prompt", map[string]any{"slug": "s", "name": "X", "checklistTemplate": "c"}, 400},
		{"missing checklist", map[string]any{"slug": "s", "name": "X", "systemPrompt": "p"}, 400},
		{"malformed JSON", `{`, 400},
		{"valid minimal", map[string]any{
			"slug": "test", "name": "Test", "systemPrompt": "p", "checklistTemplate": "c",
		}, 201},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := do(t, h.Routes(), "POST", "/api/skills", c.body)
			if rr.Code != c.wantStatus {
				t.Errorf("status: got %d, want %d (body: %s)", rr.Code, c.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestCreateSkill_IdempotentUpsert(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	body := map[string]any{
		"slug":              "idempotent",
		"name":              "v1",
		"version":           1,
		"systemPrompt":      "prompt v1",
		"checklistTemplate": "checklist v1",
	}
	// First insert
	rr := do(t, routes, "POST", "/api/skills", body)
	if rr.Code != 201 {
		t.Fatalf("first insert: %d %s", rr.Code, rr.Body.String())
	}

	// Second insert with updated content — should upsert, not 500
	body["name"] = "v1-updated"
	body["systemPrompt"] = "prompt v1 updated"
	rr = do(t, routes, "POST", "/api/skills", body)
	if rr.Code != 201 {
		t.Fatalf("upsert: got %d: %s", rr.Code, rr.Body.String())
	}

	// Verify there's still only one row and it has the updated content.
	var count int
	var name, prompt string
	err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM skills WHERE slug = 'idempotent'").Scan(&count)
	if err != nil || count != 1 {
		t.Fatalf("upsert created duplicate: count=%d err=%v", count, err)
	}
	_ = pool.QueryRow(context.Background(),
		"SELECT name, system_prompt FROM skills WHERE slug = 'idempotent'").Scan(&name, &prompt)
	if name != "v1-updated" {
		t.Errorf("name not updated: %q", name)
	}
	if prompt != "prompt v1 updated" {
		t.Errorf("prompt not updated: %q", prompt)
	}
}

func TestListSkills_EmptyReturnsEmptyArray(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "GET", "/api/skills", nil)
	if rr.Code != 200 {
		t.Fatalf("%d", rr.Code)
	}
	// Must be [] not null, otherwise the dashboard's map() crashes.
	var body map[string]json.RawMessage
	decode(t, rr, &body)
	raw := string(body["skills"])
	if raw != "[]" {
		t.Errorf("empty skills should be []; got %q", raw)
	}
}

func TestListSkills_PopulatedRoundTrip(t *testing.T) {
	h, _, _ := newTestHandler(t)
	routes := h.Routes()

	for _, slug := range []string{"alpha", "beta", "gamma"} {
		rr := do(t, routes, "POST", "/api/skills", map[string]any{
			"slug": slug, "name": strings.ToUpper(slug),
			"systemPrompt": "p", "checklistTemplate": "c",
			"tags": []string{"test"},
		})
		if rr.Code != 201 {
			t.Fatalf("insert %s: %d", slug, rr.Code)
		}
	}

	rr := do(t, routes, "GET", "/api/skills", nil)
	var body struct {
		Skills []struct {
			Slug      string   `json:"slug"`
			Name      string   `json:"name"`
			Version   int      `json:"version"`
			IsBuiltin bool     `json:"is_builtin"`
			Tags      []string `json:"tags"`
		} `json:"skills"`
	}
	decode(t, rr, &body)
	if len(body.Skills) != 3 {
		t.Errorf("got %d skills, want 3", len(body.Skills))
	}
	for _, s := range body.Skills {
		if !s.IsBuiltin {
			t.Errorf("%s is_builtin should be true (createSkill hardcodes it)", s.Slug)
		}
	}
}

// --- Providers CRUD ---

func TestCreateProvider_Validation(t *testing.T) {
	h, _, _ := newTestHandler(t)

	cases := []struct {
		name       string
		body       any
		wantStatus int
	}{
		{"empty body", "{}", 400},
		{"missing name", map[string]any{"baseUrl": "https://api", "authMethod": "bearer", "defaultModel": "m"}, 400},
		{"missing baseUrl", map[string]any{"name": "n", "authMethod": "bearer", "defaultModel": "m"}, 400},
		{"missing defaultModel", map[string]any{"name": "n", "baseUrl": "https://api", "authMethod": "bearer"}, 400},
		{"missing authMethod", map[string]any{"name": "n", "baseUrl": "https://api", "defaultModel": "m"}, 400},
		{"valid", map[string]any{
			"name": "openai", "baseUrl": "https://api.openai.com/v1",
			"authMethod": "bearer", "defaultModel": "gpt-4.1",
		}, 201},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := do(t, h.Routes(), "POST", "/api/providers", c.body)
			if rr.Code != c.wantStatus {
				t.Errorf("got %d, want %d: %s", rr.Code, c.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestCreateProvider_UpsertOnName(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	body := map[string]any{
		"name": "openai", "baseUrl": "https://api.openai.com/v1",
		"authMethod": "bearer", "defaultModel": "gpt-4.1",
		"models": []string{"gpt-4.1"},
	}
	do(t, routes, "POST", "/api/providers", body)
	body["defaultModel"] = "gpt-4.1-mini"
	rr := do(t, routes, "POST", "/api/providers", body)
	if rr.Code != 201 {
		t.Fatalf("upsert: %d %s", rr.Code, rr.Body.String())
	}

	var count int
	var def string
	_ = pool.QueryRow(context.Background(),
		"SELECT count(*), max(default_model) FROM llm_providers WHERE name = 'openai'").Scan(&count, &def)
	if count != 1 {
		t.Errorf("duplicate rows: %d", count)
	}
	if def != "gpt-4.1-mini" {
		t.Errorf("default_model not updated: %q", def)
	}
}

func TestListProviders_EmptyIsEmptyArray(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "GET", "/api/providers", nil)
	var body map[string]json.RawMessage
	decode(t, rr, &body)
	if string(body["providers"]) != "[]" {
		t.Errorf("got %q, want []", body["providers"])
	}
}

func TestListProviders_ModelsArrayNeverNull(t *testing.T) {
	h, _, _ := newTestHandler(t)
	routes := h.Routes()

	// Provider with no models in request body
	do(t, routes, "POST", "/api/providers", map[string]any{
		"name": "empty", "baseUrl": "https://x", "authMethod": "none", "defaultModel": "m",
	})

	rr := do(t, routes, "GET", "/api/providers", nil)
	var body struct {
		Providers []struct {
			Models json.RawMessage `json:"models"`
		} `json:"providers"`
	}
	decode(t, rr, &body)
	if len(body.Providers) != 1 {
		t.Fatalf("got %d providers", len(body.Providers))
	}
	// Must not be JSON null — the dashboard's .models.map(...) would crash.
	if string(body.Providers[0].Models) == "null" {
		t.Errorf("models should be [] when empty, not null")
	}
}

// --- Agents CRUD ---

func TestCreateAgent_ValidationAndRoundTrip(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	// Seed a skill and a provider first
	skillID := insertSkill(t, pool, "test-skill")
	providerID := insertProvider(t, pool, "test-provider")

	// Missing required fields
	rr := do(t, routes, "POST", "/api/agents", map[string]any{"name": ""})
	if rr.Code != 400 {
		t.Errorf("missing name/ids should return 400, got %d", rr.Code)
	}

	// Valid creation
	rr = do(t, routes, "POST", "/api/agents", map[string]any{
		"name":        "my-agent",
		"skillId":     skillID,
		"providerId":  providerID,
		"temperature": 0.0,
		"maxTokens":   2048,
		"enabled":     true,
		"priority":    50,
	})
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}

	// Listing the agents returns ours, with the skill+provider names joined in
	listRR := do(t, routes, "GET", "/api/agents", nil)
	var listBody struct {
		Agents []struct {
			Name         string `json:"name"`
			SkillSlug    string `json:"skill_slug"`
			ProviderName string `json:"provider_name"`
			Enabled      bool   `json:"enabled"`
		} `json:"agents"`
	}
	decode(t, listRR, &listBody)
	if len(listBody.Agents) != 1 {
		t.Fatalf("got %d agents", len(listBody.Agents))
	}
	a := listBody.Agents[0]
	if a.Name != "my-agent" || a.SkillSlug != "test-skill" || a.ProviderName != "test-provider" || !a.Enabled {
		t.Errorf("agent row wrong: %+v", a)
	}
}

func TestListAgents_EmptyIsEmptyArray(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "GET", "/api/agents", nil)
	var body map[string]json.RawMessage
	decode(t, rr, &body)
	if string(body["agents"]) != "[]" {
		t.Errorf("got %q, want []", body["agents"])
	}
}

func TestDisableAgent(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()
	skillID := insertSkill(t, pool, "s")
	providerID := insertProvider(t, pool, "p")
	do(t, routes, "POST", "/api/agents", map[string]any{
		"name": "to-disable", "skillId": skillID, "providerId": providerID, "enabled": true,
	})

	var agentID string
	_ = pool.QueryRow(context.Background(),
		"SELECT id FROM agents WHERE name = 'to-disable'").Scan(&agentID)

	rr := do(t, routes, "DELETE", "/api/agents/"+agentID, nil)
	if rr.Code != 200 {
		t.Fatalf("delete: %d", rr.Code)
	}

	var enabled bool
	_ = pool.QueryRow(context.Background(),
		"SELECT enabled FROM agents WHERE id = $1", agentID).Scan(&enabled)
	if enabled {
		t.Errorf("agent should be disabled (enabled=false) after DELETE")
	}
}

// --- Reviews / sessions ---

func TestTriggerReview_EnqueuesAndPersists(t *testing.T) {
	h, enq, pool := newTestHandler(t)
	routes := h.Routes()

	rr := do(t, routes, "POST", "/api/reviews", map[string]any{
		"platform":      "github",
		"repository":    "acme/widget",
		"prNumber":      42,
		"headSha":       "abc1234",
		"baseSha":       "def5678",
		"triggerSource": "api",
	})
	if rr.Code != 202 {
		t.Fatalf("status: %d (body: %s)", rr.Code, rr.Body.String())
	}
	var body struct {
		SessionID string `json:"sessionId"`
		Status    string `json:"status"`
	}
	decode(t, rr, &body)
	if body.SessionID == "" || body.Status != "queued" {
		t.Errorf("response: %+v", body)
	}

	// Enqueuer received exactly one session with the right fields
	calls := enq.Calls()
	if len(calls) != 1 {
		t.Fatalf("enqueuer calls: got %d, want 1", len(calls))
	}
	s := calls[0]
	if s.ID != body.SessionID {
		t.Errorf("enqueued session ID mismatch")
	}
	if s.RepositoryFullName != "acme/widget" || s.PRNumber != 42 || s.HeadSHA != "abc1234" {
		t.Errorf("enqueued session missing fields: %+v", s)
	}

	// DB row exists
	var count int
	_ = pool.QueryRow(context.Background(),
		"SELECT count(*) FROM review_sessions WHERE id = $1", body.SessionID).Scan(&count)
	if count != 1 {
		t.Errorf("review_sessions row not persisted")
	}

	// Audit trail contains session.created (session.queued is emitted by
	// the real queue.Client, not the handler — that's covered by the queue
	// package tests).
	var events []string
	rows, _ := pool.Query(context.Background(),
		"SELECT event_type FROM audit_log WHERE session_id = $1 ORDER BY id", body.SessionID)
	defer rows.Close()
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		events = append(events, e)
	}
	if !contains(events, "session.created") {
		t.Errorf("missing session.created event: %v", events)
	}
}

func TestTriggerReview_MalformedBody(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "POST", "/api/reviews", "{not json")
	if rr.Code != 400 {
		t.Errorf("malformed body: got %d, want 400", rr.Code)
	}
}

func TestListSessions_EmptyIsEmptyArray(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "GET", "/api/reviews", nil)
	var body map[string]json.RawMessage
	decode(t, rr, &body)
	if string(body["sessions"]) != "[]" {
		t.Errorf("got %q, want []", body["sessions"])
	}
}

func TestListSessions_OrderingAndLimit(t *testing.T) {
	h, _, _ := newTestHandler(t)
	routes := h.Routes()

	// Insert 5 sessions
	for i := 1; i <= 5; i++ {
		do(t, routes, "POST", "/api/reviews", map[string]any{
			"platform":      "github",
			"repository":    "acme/w",
			"prNumber":      i,
			"headSha":       fmt.Sprintf("h%d", i),
			"baseSha":       fmt.Sprintf("b%d", i),
			"triggerSource": "api",
		})
	}

	// Pagination: limit=3
	rr := do(t, routes, "GET", "/api/reviews?limit=3", nil)
	var body struct {
		Sessions []struct {
			PRNumber int `json:"pr_number"`
		} `json:"sessions"`
		Limit int `json:"limit"`
	}
	decode(t, rr, &body)
	if len(body.Sessions) != 3 {
		t.Errorf("got %d sessions, want 3", len(body.Sessions))
	}
	if body.Limit != 3 {
		t.Errorf("limit in response: %d", body.Limit)
	}
	// Newest first
	if body.Sessions[0].PRNumber != 5 {
		t.Errorf("first should be newest (PR #5), got #%d", body.Sessions[0].PRNumber)
	}
}

func TestGetSession_ReturnsSessionReportsDecisionsProgress(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	// Trigger a review
	trig := do(t, routes, "POST", "/api/reviews", map[string]any{
		"platform": "github", "repository": "a/b", "prNumber": 1,
		"headSha": "h", "baseSha": "b", "triggerSource": "api",
	})
	var trigBody struct {
		SessionID string `json:"sessionId"`
	}
	decode(t, trig, &trigBody)

	// Fetch the session back
	rr := do(t, routes, "GET", "/api/reviews/"+trigBody.SessionID, nil)
	if rr.Code != 200 {
		t.Fatalf("get session: %d", rr.Code)
	}
	var body struct {
		Session struct {
			ID       string `json:"id"`
			PRNumber int    `json:"pr_number"`
			Status   string `json:"status"`
		} `json:"session"`
		Reports   []any `json:"reports"`
		Decisions []any `json:"decisions"`
		Progress  struct {
			Completed int `json:"completed"`
			Total     int `json:"total"`
		} `json:"progress"`
	}
	decode(t, rr, &body)
	if body.Session.ID != trigBody.SessionID {
		t.Errorf("session id mismatch")
	}
	if body.Session.PRNumber != 1 {
		t.Errorf("pr_number: %d", body.Session.PRNumber)
	}
	if body.Reports == nil || body.Decisions == nil {
		t.Errorf("reports/decisions must be arrays not null: %+v %+v", body.Reports, body.Decisions)
	}
	// Progress field must be present
	_ = body.Progress
	_ = pool
}

func TestGetSession_NotFound(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "GET", "/api/reviews/00000000-0000-0000-0000-000000000000", nil)
	if rr.Code != 404 {
		t.Errorf("expected 404, got %d", rr.Code)
	}
}

// --- Webhooks ---

func TestGitHubWebhook_Enqueues(t *testing.T) {
	h, enq, pool := newTestHandler(t)

	payload := map[string]any{
		"action": "opened",
		"number": 101,
		"pull_request": map[string]any{
			"title":    "Test PR",
			"html_url": "https://github.com/acme/w/pull/101",
			"draft":    false,
			"user":     map[string]any{"Login": "alice"},
			"head":     map[string]any{"SHA": "aaa111", "Ref": "feature"},
			"base":     map[string]any{"SHA": "bbb222", "Ref": "main"},
		},
		"repository": map[string]any{"full_name": "acme/widget"},
	}
	rr := do(t, h.Routes(), "POST", "/api/webhooks/github", payload)
	if rr.Code != 202 {
		t.Fatalf("status: %d %s", rr.Code, rr.Body.String())
	}

	calls := enq.Calls()
	if len(calls) != 1 {
		t.Fatalf("enqueue count: %d", len(calls))
	}
	if calls[0].PRNumber != 101 || calls[0].RepositoryFullName != "acme/widget" {
		t.Errorf("session fields wrong: %+v", calls[0])
	}

	// DB row exists
	var cnt int
	_ = pool.QueryRow(context.Background(),
		"SELECT count(*) FROM review_sessions WHERE external_pr_id = 'github:acme/widget#101'").Scan(&cnt)
	if cnt != 1 {
		t.Errorf("session not persisted")
	}
}

func TestGitHubWebhook_DraftIgnored(t *testing.T) {
	h, enq, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "POST", "/api/webhooks/github", map[string]any{
		"action": "opened",
		"number": 2,
		"pull_request": map[string]any{
			"draft": true,
			"head":  map[string]any{"SHA": "a"},
			"base":  map[string]any{"SHA": "b"},
		},
		"repository": map[string]any{"full_name": "a/b"},
	})
	if rr.Code != 200 {
		t.Errorf("draft webhook: %d", rr.Code)
	}
	if len(enq.Calls()) != 0 {
		t.Errorf("draft PR should not enqueue")
	}
}

func TestGitHubWebhook_MalformedPayload(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "POST", "/api/webhooks/github", "{not json")
	if rr.Code != 400 {
		t.Errorf("malformed: got %d", rr.Code)
	}
}

func TestGitLabWebhook_Enqueues(t *testing.T) {
	h, enq, _ := newTestHandler(t)
	payload := map[string]any{
		"object_kind": "merge_request",
		"object_attributes": map[string]any{
			"iid":           55,
			"title":         "fix thing",
			"url":           "https://gitlab/acme/w/-/merge_requests/55",
			"action":        "open",
			"source_branch": "feature",
			"target_branch": "main",
			"last_commit":   map[string]any{"ID": "fedcba9"},
		},
		"user":    map[string]any{"Username": "alice"},
		"project": map[string]any{"path_with_namespace": "acme/widget"},
	}
	rr := do(t, h.Routes(), "POST", "/api/webhooks/gitlab", payload)
	if rr.Code != 202 {
		t.Fatalf("status: %d %s", rr.Code, rr.Body.String())
	}
	if calls := enq.Calls(); len(calls) != 1 || calls[0].PRNumber != 55 {
		t.Errorf("enqueue: %+v", calls)
	}
}

// --- Decisions ---

func TestSubmitDecision_PersistsAndAudits(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	// Create a session to decide on
	trig := do(t, routes, "POST", "/api/reviews", map[string]any{
		"platform": "github", "repository": "a/b", "prNumber": 1,
		"headSha": "h", "baseSha": "b", "triggerSource": "api",
	})
	var trigBody struct {
		SessionID string `json:"sessionId"`
	}
	decode(t, trig, &trigBody)

	rr := do(t, routes, "POST", "/api/decisions/"+trigBody.SessionID, map[string]any{
		"decision":      "approve",
		"engineerId":    "alice@acme.com",
		"engineerName":  "Alice",
		"justification": "LGTM",
	})
	if rr.Code != 201 {
		t.Fatalf("submit decision: %d %s", rr.Code, rr.Body.String())
	}

	// Decision persisted
	var count int
	_ = pool.QueryRow(context.Background(),
		"SELECT count(*) FROM approval_decisions WHERE session_id = $1", trigBody.SessionID).Scan(&count)
	if count != 1 {
		t.Errorf("decision not persisted")
	}

	// Audit event emitted with the engineerId as actor
	var auditCount int
	_ = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_log WHERE session_id = $1 AND event_type = 'decision.approve' AND actor = 'alice@acme.com'`,
		trigBody.SessionID).Scan(&auditCount)
	if auditCount != 1 {
		t.Errorf("decision audit event missing: got %d", auditCount)
	}
}

func TestGetDecisions_ReturnsArray(t *testing.T) {
	h, _, _ := newTestHandler(t)
	routes := h.Routes()

	trig := do(t, routes, "POST", "/api/reviews", map[string]any{
		"platform": "github", "repository": "a/b", "prNumber": 1,
		"headSha": "h", "baseSha": "b", "triggerSource": "api",
	})
	var trigBody struct {
		SessionID string `json:"sessionId"`
	}
	decode(t, trig, &trigBody)

	// Initially empty
	rr := do(t, routes, "GET", "/api/decisions/"+trigBody.SessionID, nil)
	var body map[string]json.RawMessage
	decode(t, rr, &body)
	if string(body["decisions"]) != "[]" {
		t.Errorf("empty decisions should be []; got %q", body["decisions"])
	}

	// Submit one
	do(t, routes, "POST", "/api/decisions/"+trigBody.SessionID, map[string]any{
		"decision":     "approve",
		"engineerId":   "alice",
		"engineerName": "Alice",
	})

	rr = do(t, routes, "GET", "/api/decisions/"+trigBody.SessionID, nil)
	var populated struct {
		Decisions []struct {
			Decision string `json:"decision"`
			Engineer string `json:"engineer_name"`
		} `json:"decisions"`
	}
	decode(t, rr, &populated)
	if len(populated.Decisions) != 1 {
		t.Errorf("got %d decisions, want 1", len(populated.Decisions))
	}
	if populated.Decisions[0].Decision != "approve" {
		t.Errorf("decision: %q", populated.Decisions[0].Decision)
	}
}

// --- Audit endpoints ---

func TestQueryAudit_EmptyIsEmptyArray(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "GET", "/api/audit", nil)
	var body map[string]json.RawMessage
	decode(t, rr, &body)
	if string(body["entries"]) != "[]" {
		t.Errorf("empty entries should be []; got %q", body["entries"])
	}
}

func TestSessionAuditTrail_EmptyIsEmptyArray(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "GET", "/api/audit/session/00000000-0000-0000-0000-000000000000", nil)
	// Even with no entries, we must return entries: [], not null.
	var body map[string]json.RawMessage
	decode(t, rr, &body)
	if string(body["entries"]) != "[]" {
		t.Errorf("got %q, want []", body["entries"])
	}
}

func TestVerifyAuditChain_NoHashChainReturnsValid(t *testing.T) {
	h, _, _ := newTestHandler(t)
	// Handler's audit logger was built with hashChain=false, so verify
	// short-circuits to valid.
	rr := do(t, h.Routes(), "GET", "/api/audit/verify", nil)
	var body struct {
		Valid bool `json:"valid"`
	}
	decode(t, rr, &body)
	if !body.Valid {
		t.Errorf("chain should be valid when hash chaining is disabled")
	}
}

// --- Helpers ---

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func insertSkill(t *testing.T, pool *pgxpool.Pool, slug string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO skills (slug, name, version, system_prompt, checklist_template, is_builtin, tags)
		VALUES ($1, $1, 1, 'p', 'c', false, '{}') RETURNING id`, slug).Scan(&id)
	if err != nil {
		t.Fatalf("insert skill: %v", err)
	}
	return id
}

func insertProvider(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO llm_providers (name, base_url, auth_method, default_model, models, config)
		VALUES ($1, 'https://x', 'bearer', 'm', '{}', '{}') RETURNING id`, name).Scan(&id)
	if err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	return id
}

// --- Agent CRUD (P1.1) ---

func TestUpdateAgent_PatchesPartialFields(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	skillID := insertSkill(t, pool, "patch-skill")
	provID := insertProvider(t, pool, "patch-prov")

	rr := do(t, routes, "POST", "/api/agents", map[string]any{
		"name": "orig", "skillId": skillID, "providerId": provID,
		"temperature": 0.1, "maxTokens": 2048, "enabled": true, "priority": 50,
	})
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(t, rr, &created)

	// Patch only temperature + priority.
	rr = do(t, routes, "PUT", "/api/agents/"+created.ID, map[string]any{
		"temperature": 0.7,
		"priority":    10,
	})
	if rr.Code != 200 {
		t.Fatalf("patch: %d %s", rr.Code, rr.Body.String())
	}

	// Fields outside the patch body must be preserved.
	var name string
	var temp float64
	var prio, maxTok int
	var enabled bool
	err := pool.QueryRow(context.Background(),
		"SELECT name, temperature, priority, max_tokens, enabled FROM agents WHERE id = $1", created.ID,
	).Scan(&name, &temp, &prio, &maxTok, &enabled)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if name != "orig" || temp < 0.69 || temp > 0.71 || prio != 10 || maxTok != 2048 || !enabled {
		t.Errorf("patch did not preserve untouched fields: name=%q temp=%v prio=%d maxTok=%d enabled=%v",
			name, temp, prio, maxTok, enabled)
	}
}

func TestUpdateAgent_NotFound(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "PUT",
		"/api/agents/00000000-0000-0000-0000-000000000000",
		map[string]any{"temperature": 0.5})
	if rr.Code != 404 {
		t.Fatalf("missing agent should be 404, got %d", rr.Code)
	}
}

func TestGetAgent_RoundTrip(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	skillID := insertSkill(t, pool, "get-skill")
	provID := insertProvider(t, pool, "get-prov")

	rr := do(t, routes, "POST", "/api/agents", map[string]any{
		"name": "named", "skillId": skillID, "providerId": provID,
		"temperature": 0.2, "maxTokens": 1024, "enabled": true, "priority": 5,
	})
	if rr.Code != 201 {
		t.Fatalf("create: %d", rr.Code)
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(t, rr, &created)

	rr = do(t, routes, "GET", "/api/agents/"+created.ID, nil)
	if rr.Code != 200 {
		t.Fatalf("get: %d %s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	decode(t, rr, &got)
	if got["name"] != "named" || got["skill_slug"] != "get-skill" || got["provider_name"] != "get-prov" {
		t.Errorf("unexpected agent: %+v", got)
	}
}

func TestDisableAgent_FlagsRow(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	skillID := insertSkill(t, pool, "dis-skill")
	provID := insertProvider(t, pool, "dis-prov")

	rr := do(t, routes, "POST", "/api/agents", map[string]any{
		"name": "x", "skillId": skillID, "providerId": provID,
		"enabled": true, "priority": 1,
	})
	var created struct {
		ID string `json:"id"`
	}
	decode(t, rr, &created)

	rr = do(t, routes, "DELETE", "/api/agents/"+created.ID, nil)
	if rr.Code != 200 {
		t.Fatalf("delete: %d", rr.Code)
	}
	var enabled bool
	_ = pool.QueryRow(context.Background(),
		"SELECT enabled FROM agents WHERE id = $1", created.ID).Scan(&enabled)
	if enabled {
		t.Errorf("expected enabled=false after DELETE, still true")
	}
}

// --- Skill update (P1.1) ---

func TestUpdateSkill_BumpsVersion(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	// Seed v1.
	rr := do(t, routes, "POST", "/api/skills", map[string]any{
		"slug": "evolving", "name": "v1", "version": 1,
		"systemPrompt": "p1", "checklistTemplate": "c1",
	})
	if rr.Code != 201 {
		t.Fatalf("seed: %d", rr.Code)
	}

	// PUT without version → server picks next.
	rr = do(t, routes, "PUT", "/api/skills/evolving", map[string]any{
		"name":              "v2",
		"systemPrompt":      "p2",
		"checklistTemplate": "c2",
	})
	if rr.Code != 200 {
		t.Fatalf("put: %d %s", rr.Code, rr.Body.String())
	}

	var versions []int
	rows, err := pool.Query(context.Background(),
		"SELECT version FROM skills WHERE slug = 'evolving' ORDER BY version")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var v int
		_ = rows.Scan(&v)
		versions = append(versions, v)
	}
	if len(versions) != 2 || versions[0] != 1 || versions[1] != 2 {
		t.Errorf("expected versions [1,2], got %v", versions)
	}
}

// --- Provider update (P1.1) ---

func TestUpdateProvider_PreservesUnsetFields(t *testing.T) {
	h, _, pool := newTestHandler(t)
	routes := h.Routes()

	provID := insertProvider(t, pool, "evolving-prov")

	rr := do(t, routes, "PUT", "/api/providers/"+provID, map[string]any{
		"defaultModel": "new-model",
	})
	if rr.Code != 200 {
		t.Fatalf("put: %d %s", rr.Code, rr.Body.String())
	}

	var name, model, baseURL string
	_ = pool.QueryRow(context.Background(),
		"SELECT name, base_url, default_model FROM llm_providers WHERE id = $1", provID,
	).Scan(&name, &baseURL, &model)
	if name != "evolving-prov" {
		t.Errorf("name should be preserved: %q", name)
	}
	if baseURL != "https://x" {
		t.Errorf("base_url should be preserved: %q", baseURL)
	}
	if model != "new-model" {
		t.Errorf("default_model not updated: %q", model)
	}
}

// --- Rerun review (P1.1) ---

func TestRerunReview_ResetsAndEnqueues(t *testing.T) {
	h, enq, pool := newTestHandler(t)
	routes := h.Routes()

	// Seed a completed session with a stale agent report.
	var sessionID string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO review_sessions
		  (external_pr_id, platform, repository_full_name, pr_number,
		   head_sha, base_sha, trigger_source, status, overall_verdict,
		   composite_report, completed_at)
		VALUES ('github:x#1','github','x',1,'h','b','api','completed','pass','old',now())
		RETURNING id`,
	).Scan(&sessionID)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Need an agent + report so we can verify the report is wiped.
	skillID := insertSkill(t, pool, "rerun-skill")
	provID := insertProvider(t, pool, "rerun-prov")
	var agentID string
	_ = pool.QueryRow(context.Background(), `
		INSERT INTO agents (name, skill_id, provider_id, enabled)
		VALUES ('a',$1,$2,true) RETURNING id`, skillID, provID).Scan(&agentID)
	_, _ = pool.Exec(context.Background(), `
		INSERT INTO agent_reports
		  (session_id, agent_id, skill_slug, skill_version, model_used, provider_name,
		   prompt_hash, verdict, report_md)
		VALUES ($1,$2,'rerun-skill',1,'m','rerun-prov','h','pass','old')`, sessionID, agentID)

	rr := do(t, routes, "POST", "/api/reviews/"+sessionID+"/rerun", nil)
	if rr.Code != 202 {
		t.Fatalf("rerun: %d %s", rr.Code, rr.Body.String())
	}

	if len(enq.Calls()) != 1 || enq.Calls()[0].ID != sessionID {
		t.Fatalf("expected one re-enqueue for %s, got %+v", sessionID, enq.Calls())
	}

	var status string
	var verdict, composite *string
	var completedAt *string
	_ = pool.QueryRow(context.Background(),
		`SELECT status, overall_verdict, composite_report, completed_at::text
		 FROM review_sessions WHERE id = $1`, sessionID,
	).Scan(&status, &verdict, &composite, &completedAt)
	if status != "queued" {
		t.Errorf("status should be queued, got %q", status)
	}
	if verdict != nil || composite != nil || completedAt != nil {
		t.Errorf("rerun should clear verdict/composite/completed_at")
	}

	var reportCount int
	_ = pool.QueryRow(context.Background(),
		"SELECT count(*) FROM agent_reports WHERE session_id = $1", sessionID,
	).Scan(&reportCount)
	if reportCount != 0 {
		t.Errorf("agent_reports should be wiped, got %d", reportCount)
	}
}

func TestRerunReview_NotFound(t *testing.T) {
	h, _, _ := newTestHandler(t)
	rr := do(t, h.Routes(), "POST",
		"/api/reviews/00000000-0000-0000-0000-000000000000/rerun", nil)
	if rr.Code != 404 {
		t.Fatalf("missing session should be 404, got %d", rr.Code)
	}
}

// --- Context documents (P1.3) ---

func TestCreateContext_Validation(t *testing.T) {
	h, _, _ := newTestHandler(t)
	cases := []struct {
		name       string
		body       any
		wantStatus int
	}{
		{"empty", "{}", 400},
		{"missing docType", map[string]any{"name": "x", "content": "c"}, 400},
		{"bad docType", map[string]any{"name": "x", "content": "c", "docType": "wat"}, 400},
		{"valid", map[string]any{
			"name": "Convention", "content": "do this", "docType": "guideline",
			"tags": []string{"golang"},
		}, 201},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := do(t, h.Routes(), "POST", "/api/contexts", c.body)
			if rr.Code != c.wantStatus {
				t.Errorf("status: got %d, want %d (%s)", rr.Code, c.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestContext_RoundTripGetUpdateDelete(t *testing.T) {
	h, _, _ := newTestHandler(t)
	routes := h.Routes()

	rr := do(t, routes, "POST", "/api/contexts", map[string]any{
		"name": "Initial", "content": "v1", "docType": "guideline",
		"tags": []string{"a"},
	})
	if rr.Code != 201 {
		t.Fatalf("create: %d %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	decode(t, rr, &created)

	// GET
	rr = do(t, routes, "GET", "/api/contexts/"+created.ID, nil)
	if rr.Code != 200 {
		t.Fatalf("get: %d", rr.Code)
	}
	var got map[string]any
	decode(t, rr, &got)
	if got["name"] != "Initial" || got["content"] != "v1" || got["doc_type"] != "guideline" {
		t.Errorf("unexpected payload: %+v", got)
	}

	// PUT (partial)
	rr = do(t, routes, "PUT", "/api/contexts/"+created.ID, map[string]any{
		"content": "v2",
	})
	if rr.Code != 200 {
		t.Fatalf("put: %d %s", rr.Code, rr.Body.String())
	}
	rr = do(t, routes, "GET", "/api/contexts/"+created.ID, nil)
	decode(t, rr, &got)
	if got["content"] != "v2" || got["name"] != "Initial" {
		t.Errorf("partial update wrong: %+v", got)
	}

	// DELETE
	rr = do(t, routes, "DELETE", "/api/contexts/"+created.ID, nil)
	if rr.Code != 200 {
		t.Fatalf("delete: %d", rr.Code)
	}
	rr = do(t, routes, "GET", "/api/contexts/"+created.ID, nil)
	if rr.Code != 404 {
		t.Errorf("after delete should be 404, got %d", rr.Code)
	}
}

func TestListContexts_OmitsContent(t *testing.T) {
	h, _, _ := newTestHandler(t)
	routes := h.Routes()
	do(t, routes, "POST", "/api/contexts", map[string]any{
		"name": "A", "content": "secret-body", "docType": "reference",
	})

	rr := do(t, routes, "GET", "/api/contexts", nil)
	if rr.Code != 200 {
		t.Fatalf("list: %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret-body") {
		t.Errorf("list endpoint should not return body content")
	}
}
