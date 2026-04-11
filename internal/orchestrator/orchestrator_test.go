package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/phalanx-ai/phalanx/internal/audit"
	"github.com/phalanx-ai/phalanx/internal/llm"
	"github.com/phalanx-ai/phalanx/internal/platform"
	"github.com/phalanx-ai/phalanx/internal/report"
	"github.com/phalanx-ai/phalanx/internal/types"
)

// --- Fixtures ---

// stubAdapter returns programmable canned responses keyed by skill slug
// (extracted from the user message via a simple substring match).
type stubAdapter struct {
	mu        sync.Mutex
	calls     int
	responses map[string]*types.LLMResponse // key: skill slug substring
	err       error
}

func (s *stubAdapter) Complete(ctx context.Context, req types.LLMRequest, provider types.LLMProvider) (*types.LLMResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++

	if s.err != nil {
		return nil, s.err
	}
	// Look at the system prompt to pick a response — crude but fine for tests
	sys := ""
	if len(req.Messages) > 0 {
		sys = req.Messages[0].Content
	}
	for key, resp := range s.responses {
		if containsInsensitive(sys, key) {
			copy := *resp
			copy.Model = req.Model
			return &copy, nil
		}
	}
	// Default: pass
	return &types.LLMResponse{
		Content:      "**Verdict:** pass\n\n- [x] default check\n",
		Model:        req.Model,
		InputTokens:  10,
		OutputTokens: 5,
	}, nil
}

func containsInsensitive(haystack, needle string) bool {
	return len(haystack) > 0 && len(needle) > 0 &&
		indexFold(haystack, needle) >= 0
}

func indexFold(s, substr string) int {
	// lowercase search without importing strings.ToLower to keep this
	// focused — skills prompts are already lowercase-friendly.
	for i := 0; i+len(substr) <= len(s); i++ {
		match := true
		for j := 0; j < len(substr); j++ {
			a, b := s[i+j], substr[j]
			if a >= 'A' && a <= 'Z' {
				a += 32
			}
			if b >= 'A' && b <= 'Z' {
				b += 32
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// fakePlatform records PostReview calls so we can assert the orchestrator
// actually hands the report back to the git host.
type fakePlatform struct {
	mu              sync.Mutex
	fetchDiffResult *platform.DiffResult
	fetchDiffErr    error
	postReviewErr   error
	postedReports   []types.CompositeReport
}

func (f *fakePlatform) FetchDiff(ctx context.Context, repo, baseSHA, headSHA string) (*platform.DiffResult, error) {
	if f.fetchDiffErr != nil {
		return nil, f.fetchDiffErr
	}
	if f.fetchDiffResult != nil {
		return f.fetchDiffResult, nil
	}
	return &platform.DiffResult{
		Diff: "--- a/foo.go\n+++ b/foo.go\n@@ -1 +1 @@\n-old\n+new\n",
		Files: []types.FileEntry{
			{Path: "foo.go", Status: "modified", Additions: 1, Deletions: 1},
		},
	}, nil
}

func (f *fakePlatform) PostReview(ctx context.Context, session types.ReviewSession, r types.CompositeReport) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.postReviewErr != nil {
		return f.postReviewErr
	}
	f.postedReports = append(f.postedReports, r)
	return nil
}

func (f *fakePlatform) VerifyUser(ctx context.Context, token string) (*platform.UserInfo, error) {
	return &platform.UserInfo{ID: "fake", Login: "fake"}, nil
}

func (f *fakePlatform) Posted() []types.CompositeReport {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]types.CompositeReport, len(f.postedReports))
	copy(out, f.postedReports)
	return out
}

// --- Test DB setup ---

func openTestDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("PHALANX_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("PHALANX_TEST_DATABASE_URL not set; skipping orchestrator integration test")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func freshDB(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		TRUNCATE
		  audit_log, approval_decisions, agent_reports, review_sessions,
		  agent_context, agents, context_documents, skills, llm_providers
		RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
}

type orchFixture struct {
	pool     *pgxpool.Pool
	orch     *Orchestrator
	adapter  *stubAdapter
	platform *fakePlatform
	auditLog *audit.Logger
}

func setupOrchestrator(t *testing.T) *orchFixture {
	t.Helper()
	pool := openTestDB(t)
	freshDB(t, pool)

	auditLog := audit.New(pool, false)
	adapter := &stubAdapter{responses: map[string]*types.LLMResponse{}}
	router := llm.NewRouter(auditLog)

	provider := types.LLMProvider{
		ID:           newTestUUID(t, pool, "provider-seed"),
		Name:         "fake-provider",
		BaseURL:      "https://fake",
		DefaultModel: "fake-model",
		Config: types.ProviderConfig{
			MaxRetries:        -1, // no retries in tests
			RequestsPerMinute: 60000,
		},
	}
	router.RegisterProvider(provider, adapter)

	platforms := map[types.Platform]platform.Client{
		types.PlatformGitHub: &fakePlatform{},
	}

	orch := New(pool, auditLog, router, &report.Builder{}, platforms, 10)

	return &orchFixture{
		pool:     pool,
		orch:     orch,
		adapter:  adapter,
		platform: platforms[types.PlatformGitHub].(*fakePlatform),
		auditLog: auditLog,
	}
}

// seedSkillProviderAgent inserts a minimal skill+provider+agent triple and
// returns the agent ID. The provider is reused if already inserted.
func (f *orchFixture) seedSkillProviderAgent(t *testing.T, slug, name string, enabled bool) (skillID, providerID, agentID string) {
	t.Helper()
	ctx := context.Background()

	// Skill
	err := f.pool.QueryRow(ctx, `
		INSERT INTO skills (slug, name, version, system_prompt, checklist_template, is_builtin, tags)
		VALUES ($1, $2, 1, 'Test prompt for ' || $1, '## {{verdict}}', true, '{}')
		RETURNING id`, slug, name).Scan(&skillID)
	if err != nil {
		t.Fatalf("insert skill: %v", err)
	}

	// Provider (upsert to reuse)
	err = f.pool.QueryRow(ctx, `
		INSERT INTO llm_providers (name, base_url, auth_method, default_model, models, config)
		VALUES ('fake-provider', 'https://fake', 'none', 'fake-model', '{}', '{}')
		ON CONFLICT (name) DO UPDATE SET updated_at = now()
		RETURNING id`).Scan(&providerID)
	if err != nil {
		t.Fatalf("insert provider: %v", err)
	}

	// Agent
	err = f.pool.QueryRow(ctx, `
		INSERT INTO agents (name, skill_id, provider_id, temperature, max_tokens, enabled, priority, config)
		VALUES ($1, $2, $3, 0, 1024, $4, 100, '{}')
		RETURNING id`, slug+"-agent", skillID, providerID, enabled).Scan(&agentID)
	if err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	return
}

func (f *orchFixture) insertSession(t *testing.T, platform types.Platform, prNumber int, diff string) types.ReviewSession {
	t.Helper()
	ctx := context.Background()
	s := types.ReviewSession{
		ExternalPRID:       "test#1",
		Platform:           platform,
		RepositoryFullName: "acme/widget",
		PRNumber:           prNumber,
		HeadSHA:            "aabbccdd11223344",
		BaseSHA:            "ffeeddcc99887766",
		DiffSnapshot:       &diff,
		TriggerSource:      types.TriggerAPI,
		Status:             types.StatusQueued,
	}
	err := f.pool.QueryRow(ctx, `
		INSERT INTO review_sessions
		  (external_pr_id, platform, repository_full_name, pr_number,
		   head_sha, base_sha, diff_snapshot, trigger_source, status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING id, started_at`,
		s.ExternalPRID, s.Platform, s.RepositoryFullName, s.PRNumber,
		s.HeadSHA, s.BaseSHA, s.DiffSnapshot, s.TriggerSource, s.Status).Scan(&s.ID, &s.StartedAt)
	if err != nil {
		t.Fatalf("insert session: %v", err)
	}
	return s
}

func newTestUUID(t *testing.T, pool *pgxpool.Pool, _ string) string {
	t.Helper()
	var id string
	err := pool.QueryRow(context.Background(), `SELECT gen_random_uuid()`).Scan(&id)
	if err != nil {
		t.Fatalf("gen uuid: %v", err)
	}
	return id
}

// --- Tests ---

func TestExecuteReview_HappyPath(t *testing.T) {
	f := setupOrchestrator(t)

	// Seed two agents with different outcomes
	f.adapter.responses = map[string]*types.LLMResponse{
		"security": {
			Content:      "**Verdict:** pass\n\n- [x] no secrets\n",
			InputTokens:  100,
			OutputTokens: 20,
		},
		"accessibility": {
			Content:      "**Verdict:** warn\n\n- [~] some concerns\n",
			InputTokens:  80,
			OutputTokens: 15,
		},
	}

	f.seedSkillProviderAgent(t, "security", "Security", true)
	f.seedSkillProviderAgent(t, "accessibility", "Accessibility", true)

	session := f.insertSession(t, types.PlatformGitHub, 1, "some diff")

	composite, err := f.orch.ExecuteReview(context.Background(), session)
	if err != nil {
		t.Fatalf("ExecuteReview: %v", err)
	}

	// Composite report returned
	if composite == nil {
		t.Fatal("nil composite report")
	}
	if composite.OverallVerdict != types.VerdictWarn {
		t.Errorf("overall verdict: got %s, want warn (one agent warned)", composite.OverallVerdict)
	}
	if len(composite.AgentSummaries) != 2 {
		t.Errorf("summaries: got %d, want 2", len(composite.AgentSummaries))
	}

	// Adapter called once per agent
	if f.adapter.calls != 2 {
		t.Errorf("adapter calls: got %d, want 2", f.adapter.calls)
	}

	// Session row updated to completed
	var status, verdict string
	_ = f.pool.QueryRow(context.Background(),
		`SELECT status, overall_verdict FROM review_sessions WHERE id = $1`, session.ID).
		Scan(&status, &verdict)
	if status != "completed" {
		t.Errorf("session status: %q", status)
	}
	if verdict != "warn" {
		t.Errorf("session verdict: %q", verdict)
	}

	// Agent reports persisted
	var reportCount int
	_ = f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_reports WHERE session_id = $1`, session.ID).Scan(&reportCount)
	if reportCount != 2 {
		t.Errorf("agent_reports count: got %d, want 2", reportCount)
	}

	// Platform client was called once with the composite
	posted := f.platform.Posted()
	if len(posted) != 1 {
		t.Errorf("platform.PostReview calls: got %d, want 1", len(posted))
	}

	// Audit events include session.created (from our seed), session.running,
	// agent.started x2, agent.completed x2, report.posted, session.completed
	events := countAuditEvents(t, f.pool, session.ID)
	for _, expected := range []string{"session.running", "agent.started", "agent.completed", "report.posted", "session.completed"} {
		if events[expected] == 0 {
			t.Errorf("missing audit event %q: %+v", expected, events)
		}
	}
	if events["agent.started"] != 2 {
		t.Errorf("agent.started count: got %d, want 2", events["agent.started"])
	}
}

func TestExecuteReview_AllPass(t *testing.T) {
	f := setupOrchestrator(t)
	f.adapter.responses = map[string]*types.LLMResponse{
		"security": {Content: "**Verdict:** pass\n- [x] ok\n"},
	}
	f.seedSkillProviderAgent(t, "security", "Sec", true)
	session := f.insertSession(t, types.PlatformGitHub, 1, "diff")

	composite, err := f.orch.ExecuteReview(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if composite.OverallVerdict != types.VerdictPass {
		t.Errorf("expected overall verdict pass, got %s", composite.OverallVerdict)
	}
}

func TestExecuteReview_AllFailLeadsToFailOverall(t *testing.T) {
	f := setupOrchestrator(t)
	f.adapter.responses = map[string]*types.LLMResponse{
		"security": {Content: "**Verdict:** fail\n- [ ] critical issue\n"},
	}
	f.seedSkillProviderAgent(t, "security", "Sec", true)
	session := f.insertSession(t, types.PlatformGitHub, 1, "diff")

	composite, _ := f.orch.ExecuteReview(context.Background(), session)
	if composite.OverallVerdict != types.VerdictFail {
		t.Errorf("got %s, want fail", composite.OverallVerdict)
	}
}

func TestExecuteReview_LLMErrorPerAgentDoesNotAbortOthers(t *testing.T) {
	f := setupOrchestrator(t)
	// Make the adapter fail for every call — the orchestrator should
	// still complete with error reports for every agent, not abort.
	f.adapter.err = errors.New("upstream 500")
	f.seedSkillProviderAgent(t, "security", "Sec", true)
	f.seedSkillProviderAgent(t, "performance", "Perf", true)

	session := f.insertSession(t, types.PlatformGitHub, 1, "diff")

	composite, err := f.orch.ExecuteReview(context.Background(), session)
	if err != nil {
		t.Fatalf("ExecuteReview should not error out on agent failures: %v", err)
	}
	if composite.OverallVerdict != types.VerdictFail {
		t.Errorf("agent errors should roll up to fail verdict, got %s", composite.OverallVerdict)
	}

	// Both agents got error reports
	var errorCount int
	_ = f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM agent_reports WHERE session_id = $1 AND verdict = 'error'`, session.ID).
		Scan(&errorCount)
	if errorCount != 2 {
		t.Errorf("expected 2 error reports, got %d", errorCount)
	}
}

func TestExecuteReview_DisabledAgentsSkipped(t *testing.T) {
	f := setupOrchestrator(t)
	f.seedSkillProviderAgent(t, "enabled-skill", "Enabled", true)
	f.seedSkillProviderAgent(t, "disabled-skill", "Disabled", false)

	session := f.insertSession(t, types.PlatformGitHub, 1, "diff")
	composite, err := f.orch.ExecuteReview(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if len(composite.AgentSummaries) != 1 {
		t.Errorf("disabled agent was run: got %d summaries", len(composite.AgentSummaries))
	}
	if f.adapter.calls != 1 {
		t.Errorf("adapter called for disabled agent: %d calls", f.adapter.calls)
	}
}

func TestExecuteReview_NoAgentsStillCompletes(t *testing.T) {
	f := setupOrchestrator(t)
	session := f.insertSession(t, types.PlatformGitHub, 1, "diff")

	composite, err := f.orch.ExecuteReview(context.Background(), session)
	if err != nil {
		t.Fatalf("no-agents review should not error: %v", err)
	}
	if composite.OverallVerdict != types.VerdictPass {
		// With zero reports, overall should default to pass (nothing failed)
		t.Errorf("no agents → expected pass, got %s", composite.OverallVerdict)
	}

	var status string
	_ = f.pool.QueryRow(context.Background(),
		`SELECT status FROM review_sessions WHERE id = $1`, session.ID).Scan(&status)
	if status != "completed" {
		t.Errorf("session status: %q", status)
	}
}

func TestExecuteReview_FetchesDiffWhenSnapshotMissing(t *testing.T) {
	f := setupOrchestrator(t)
	f.seedSkillProviderAgent(t, "security", "Sec", true)

	// Insert a session without a diff_snapshot
	ctx := context.Background()
	var sessionID string
	err := f.pool.QueryRow(ctx, `
		INSERT INTO review_sessions
		  (external_pr_id, platform, repository_full_name, pr_number,
		   head_sha, base_sha, trigger_source, status)
		VALUES ('t', 'github', 'a/b', 1, 'aaaa1111', 'bbbb2222', 'api', 'queued')
		RETURNING id`).Scan(&sessionID)
	if err != nil {
		t.Fatal(err)
	}

	session := types.ReviewSession{
		ID:                 sessionID,
		Platform:           types.PlatformGitHub,
		RepositoryFullName: "a/b",
		HeadSHA:            "aaaa1111",
		BaseSHA:            "bbbb2222",
	}
	_, err = f.orch.ExecuteReview(ctx, session)
	if err != nil {
		t.Fatalf("ExecuteReview: %v", err)
	}

	// The session should now have a stored diff_snapshot from the platform
	var diff *string
	_ = f.pool.QueryRow(ctx,
		`SELECT diff_snapshot FROM review_sessions WHERE id = $1`, sessionID).Scan(&diff)
	if diff == nil || *diff == "" {
		t.Errorf("diff_snapshot should be persisted after fetch, got %v", diff)
	}
}

func TestExecuteReview_FetchDiffErrorAbortsWithFailStatus(t *testing.T) {
	f := setupOrchestrator(t)
	f.platform.fetchDiffErr = errors.New("github 404")

	ctx := context.Background()
	var sessionID string
	_ = f.pool.QueryRow(ctx, `
		INSERT INTO review_sessions
		  (external_pr_id, platform, repository_full_name, pr_number,
		   head_sha, base_sha, trigger_source, status)
		VALUES ('t', 'github', 'a/b', 1, 'a', 'b', 'api', 'queued')
		RETURNING id`).Scan(&sessionID)

	session := types.ReviewSession{
		ID:                 sessionID,
		Platform:           types.PlatformGitHub,
		RepositoryFullName: "a/b",
		HeadSHA:            "a",
		BaseSHA:            "b",
	}
	_, err := f.orch.ExecuteReview(ctx, session)
	if err == nil {
		t.Fatal("expected error when diff fetch fails")
	}

	var status string
	_ = f.pool.QueryRow(ctx,
		`SELECT status FROM review_sessions WHERE id = $1`, sessionID).Scan(&status)
	if status != "failed" {
		t.Errorf("session status after diff fetch error: got %q, want failed", status)
	}
}

func TestExecuteReview_AgentContextsIncluded(t *testing.T) {
	f := setupOrchestrator(t)

	// Seed a skill + provider + agent, then attach a context document.
	skillID, _, agentID := f.seedSkillProviderAgent(t, "security", "Sec", true)

	var ctxDocID string
	_ = f.pool.QueryRow(context.Background(), `
		INSERT INTO context_documents (name, content, doc_type, tags)
		VALUES ('acme-rules', 'ACME-specific rule: no log of tokens', 'non-negotiable', '{}')
		RETURNING id`).Scan(&ctxDocID)
	_, _ = f.pool.Exec(context.Background(),
		`INSERT INTO agent_context (agent_id, context_id) VALUES ($1, $2)`, agentID, ctxDocID)
	_ = skillID

	// Record what system prompt the adapter sees
	var sawSystem string
	f.adapter.responses = map[string]*types.LLMResponse{
		"security": {Content: "**Verdict:** pass\n- [x] ok\n"},
	}
	origComplete := f.adapter
	_ = origComplete

	// Replace the adapter hook via a wrapper — simplest way is a fresh stub
	wrapper := &captureAdapter{responses: f.adapter.responses, onSystem: func(s string) { sawSystem = s }}
	router := llm.NewRouter(audit.New(f.pool, false))
	router.RegisterProvider(types.LLMProvider{
		Name: "fake-provider", BaseURL: "https://fake", DefaultModel: "fake-model",
		Config: types.ProviderConfig{MaxRetries: -1, RequestsPerMinute: 60000},
	}, wrapper)
	// Rebuild the orchestrator with the wrapped router
	orch := New(f.pool, f.auditLog, router, &report.Builder{}, map[types.Platform]platform.Client{types.PlatformGitHub: f.platform}, 10)

	session := f.insertSession(t, types.PlatformGitHub, 1, "diff")
	_, err := orch.ExecuteReview(context.Background(), session)
	if err != nil {
		t.Fatal(err)
	}
	if sawSystem == "" {
		t.Fatal("adapter was not called")
	}
	if !containsInsensitive(sawSystem, "acme-rules") {
		t.Errorf("context document not injected into system prompt: %q", sawSystem)
	}
	if !containsInsensitive(sawSystem, "no log of tokens") {
		t.Errorf("context content not injected: %q", sawSystem)
	}
}

// captureAdapter is a minimal stub that captures the system prompt.
type captureAdapter struct {
	responses map[string]*types.LLMResponse
	onSystem  func(string)
}

func (c *captureAdapter) Complete(ctx context.Context, req types.LLMRequest, _ types.LLMProvider) (*types.LLMResponse, error) {
	if len(req.Messages) > 0 && c.onSystem != nil {
		c.onSystem(req.Messages[0].Content)
	}
	for key, resp := range c.responses {
		if containsInsensitive(req.Messages[0].Content, key) {
			copy := *resp
			copy.Model = req.Model
			return &copy, nil
		}
	}
	return &types.LLMResponse{Content: "**Verdict:** pass\n", Model: req.Model, InputTokens: 1, OutputTokens: 1}, nil
}

// --- Helpers ---

func countAuditEvents(t *testing.T, pool *pgxpool.Pool, sessionID string) map[string]int {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		`SELECT event_type, count(*) FROM audit_log WHERE session_id = $1 GROUP BY event_type`, sessionID)
	if err != nil {
		t.Fatalf("count audit: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var ev string
		var n int
		_ = rows.Scan(&ev, &n)
		out[ev] = n
	}
	return out
}

// avoid unused import warning for encoding/json if we drop its use later
var _ = json.Marshal
