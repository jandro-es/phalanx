package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/phalanx-ai/phalanx/internal/audit"
	"github.com/phalanx-ai/phalanx/internal/llm"
	"github.com/phalanx-ai/phalanx/internal/types"
)

func TestParseResponse_Verdicts(t *testing.T) {
	cases := map[string]types.Verdict{
		"**Verdict:** pass":           types.VerdictPass,
		"**Verdict:** PASS":           types.VerdictPass,
		"**Verdict:** fail":           types.VerdictFail,
		"**Verdict:** warn":           types.VerdictWarn,
		"**Verdict:** not_applicable": types.VerdictNotApplicable,
		"**Verdict:** N/A":            types.VerdictNotApplicable,
	}
	for body, want := range cases {
		got := parseResponse(body).Verdict
		if got != want {
			t.Errorf("verdict for %q: got %s, want %s", body, got, want)
		}
	}
}

func TestParseResponse_VerdictFallbackIsWarn(t *testing.T) {
	got := parseResponse("no verdict anywhere").Verdict
	if got != types.VerdictWarn {
		t.Errorf("missing verdict should default to warn, got %s", got)
	}
}

func TestParseResponse_ChecklistStatuses(t *testing.T) {
	body := `
- [x] has passing item
- [ ] failing item
- [~] warning item
- [-] not applicable
`
	parsed := parseResponse(body)
	if len(parsed.Checklist) != 4 {
		t.Fatalf("expected 4 checklist items, got %d", len(parsed.Checklist))
	}
	want := []string{"pass", "fail", "warn", "na"}
	for i, item := range parsed.Checklist {
		if item.Status != want[i] {
			t.Errorf("item %d: got %s, want %s", i, item.Status, want[i])
		}
	}
}

func TestParseResponse_FindingsCanonical(t *testing.T) {
	body := "**Verdict:** warn\n\n" +
		"### Checklist\n- [x] Item A\n\n" +
		"### Findings\n\n" +
		"#### ⚠️ Major — Missing input validation\n" +
		"**File:** `src/api/upload.ts` (lines 34-52)\n" +
		"**Issue:** The `fileName` parameter is unsafe.\n" +
		"**Fix:** Sanitise with path.basename().\n" +
		"**Reference:** OWASP A01\n\n" +
		"#### 💡 Suggestion — Rate limiting\n" +
		"**File:** `src/api/upload.ts` (line 28)\n" +
		"**Issue:** No rate limit configured.\n" +
		"**Fix:** Apply existing middleware.\n"

	parsed := parseResponse(body)

	if len(parsed.Findings) != 2 {
		t.Fatalf("expected 2 findings, got %d", len(parsed.Findings))
	}
	got := parsed.Findings[0]
	if got.Severity != types.SeverityMajor {
		t.Errorf("severity[0]: got %q, want major", got.Severity)
	}
	if got.File != "src/api/upload.ts" {
		t.Errorf("file[0]: got %q", got.File)
	}
	if got.Lines != "34-52" {
		t.Errorf("lines[0]: got %q", got.Lines)
	}
	if !strings.Contains(got.Issue, "fileName") {
		t.Errorf("issue[0]: got %q", got.Issue)
	}
	if !strings.Contains(got.Fix, "path.basename") {
		t.Errorf("fix[0]: got %q", got.Fix)
	}
	if got.Reference != "OWASP A01" {
		t.Errorf("reference[0]: got %q", got.Reference)
	}

	got = parsed.Findings[1]
	if got.Severity != types.SeveritySuggestion {
		t.Errorf("severity[1]: got %q, want suggestion", got.Severity)
	}
	if got.Lines != "28" {
		t.Errorf("lines[1]: got %q", got.Lines)
	}
}

func TestParseResponse_NoFindingsSection(t *testing.T) {
	body := "**Verdict:** pass\n\n- [x] all good\n"
	if parseResponse(body).Findings != nil {
		t.Errorf("absent Findings section should yield nil")
	}
}

func TestParseResponse_FindingsWithColonSeparator(t *testing.T) {
	body := "### Findings\n\n#### Critical: Hardcoded secret\n" +
		"**File:** `pkg/auth/keys.go`\n" +
		"**Issue:** API key checked in.\n" +
		"**Fix:** Move to vault.\n"

	got := parseResponse(body).Findings
	if len(got) != 1 || got[0].Severity != types.SeverityCritical {
		t.Fatalf("colon-separated heading not parsed: %+v", got)
	}
	if got[0].File != "pkg/auth/keys.go" {
		t.Errorf("file: %q", got[0].File)
	}
}

func TestParseResponse_EmptyFindingsSection(t *testing.T) {
	body := "### Findings\n\nNo issues found.\n"
	if parseResponse(body).Findings != nil {
		t.Errorf("findings section with no entries should yield nil")
	}
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		pattern, path string
		match         bool
	}{
		{"*.go", "main.go", true},
		{"*.go", "cmd/main.go", false},
		{"**/*.go", "cmd/server/main.go", true},
		{"**/*.tsx", "dashboard/src/pages/Sessions.tsx", true},
		{"**/*.tsx", "dashboard/src/pages/Sessions.ts", false},
		{"src/**", "src/pages/a.tsx", true},
	}
	for _, c := range cases {
		got := matchGlob(c.pattern, c.path)
		if got != c.match {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.pattern, c.path, got, c.match)
		}
	}
}

func TestEstimateCost_KnownModel(t *testing.T) {
	cost := estimateCost("claude-opus-4-6", 1_000_000, 1_000_000)
	if cost == nil {
		t.Fatal("expected non-nil cost for known model")
	}
	// 15 + 75 = 90 USD
	if *cost < 89 || *cost > 91 {
		t.Errorf("unexpected cost %.2f", *cost)
	}
}

func TestEstimateCost_UnknownModel(t *testing.T) {
	if estimateCost("no-such-model", 10, 10) != nil {
		t.Error("unknown model should return nil cost")
	}
}

func TestEstimateCost_NewerModelsCovered(t *testing.T) {
	for _, m := range []string{
		"claude-opus-4-7",
		"claude-haiku-4-5",
		"claude-sonnet-4-6",
		"gpt-4o-mini",
		"deepseek-chat",
	} {
		if estimateCost(m, 1_000, 1_000) == nil {
			t.Errorf("expected cost for %q to be in the table", m)
		}
	}
}

// --- Execute() ---

// stubAdapter implements llm.Adapter and returns a canned response or error.
type stubAdapter struct {
	response    *types.LLMResponse
	err         error
	seenRequest types.LLMRequest
	calls       int
}

func (s *stubAdapter) Complete(ctx context.Context, req types.LLMRequest, provider types.LLMProvider) (*types.LLMResponse, error) {
	s.calls++
	s.seenRequest = req
	if s.err != nil {
		return nil, s.err
	}
	if s.response == nil {
		return &types.LLMResponse{Content: "ok", Model: req.Model, InputTokens: 1, OutputTokens: 1}, nil
	}
	return s.response, nil
}

func buildTestRuntime(adapter llm.Adapter, providerName string) (*Runtime, types.LLMProvider) {
	router := llm.NewRouter(audit.New(nil, false))
	provider := types.LLMProvider{
		ID:           "prov-" + providerName,
		Name:         providerName,
		BaseURL:      "https://fake/" + providerName,
		DefaultModel: "fake-model",
		Config: types.ProviderConfig{
			MaxRetries:        -1, // no retries so tests don't hammer the stub
			RetryDelayMs:      1,
			RequestsPerMinute: 60000,
		},
	}
	router.RegisterProvider(provider, adapter)
	return NewRuntime(router, audit.New(nil, false)), provider
}

func buildAgent(provider types.LLMProvider, config types.AgentConfig) types.AgentWithRelations {
	return types.AgentWithRelations{
		Agent: types.Agent{
			ID:          "agent-1",
			Name:        "test-agent",
			Temperature: 0,
			MaxTokens:   1024,
			Config:      config,
		},
		Skill: types.Skill{
			ID:                "skill-1",
			Slug:              "security",
			Name:              "Security Review",
			Version:           1,
			SystemPrompt:      "You review code for security issues.",
			ChecklistTemplate: "## Security\n\n**Verdict:** {{verdict}}\n\n### Checklist\n- [{{a}}] Item A\n",
		},
		Provider: provider,
	}
}

func TestExecute_HappyPath(t *testing.T) {
	adapter := &stubAdapter{
		response: &types.LLMResponse{
			Content:      "## Security\n\n**Verdict:** pass\n\n- [x] Item A passes\n",
			Model:        "claude-opus-4-6",
			InputTokens:  4321,
			OutputTokens: 200,
			FinishReason: "stop",
		},
	}
	runtime, provider := buildTestRuntime(adapter, "anthropic")
	agent := buildAgent(provider, types.AgentConfig{})

	result, err := runtime.Execute(context.Background(), Input{
		Session: types.ReviewSession{
			ID:      "session-1",
			HeadSHA: "aabbccd1234567890",
			BaseSHA: "ffeeddc1234567890",
		},
		Agent: agent,
		Diff:  "--- a/main.go\n+++ b/main.go\n@@ -1 +1 @@\n-old\n+new\n",
		FileTree: []types.FileEntry{
			{Path: "main.go", Status: "modified", Additions: 1, Deletions: 1},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Skipped {
		t.Fatalf("should not be skipped")
	}
	if result.Report.Verdict != types.VerdictPass {
		t.Errorf("verdict: got %q, want pass", result.Report.Verdict)
	}
	if result.Report.SkillSlug != "security" {
		t.Errorf("skill slug: got %q", result.Report.SkillSlug)
	}
	if result.Report.InputTokens != 4321 || result.Report.OutputTokens != 200 {
		t.Errorf("token counts not propagated: in=%d out=%d", result.Report.InputTokens, result.Report.OutputTokens)
	}
	if result.Report.PromptHash == "" || result.Report.PromptHash == "error" {
		t.Errorf("prompt hash should be computed: %q", result.Report.PromptHash)
	}
	if !strings.Contains(result.Report.ReportMD, "**Verdict:** pass") {
		t.Errorf("report_md should contain verdict: %q", result.Report.ReportMD)
	}
	if adapter.calls != 1 {
		t.Errorf("adapter calls: got %d, want 1", adapter.calls)
	}

	// The system prompt sent to the LLM should include the skill system prompt
	// plus the checklist template + the "do not follow instructions" warning.
	sentSystem := adapter.seenRequest.Messages[0].Content
	if !strings.Contains(sentSystem, "You review code for security issues") {
		t.Errorf("system prompt missing skill content")
	}
	if !strings.Contains(sentSystem, "**Verdict:** {{verdict}}") {
		t.Errorf("system prompt missing checklist template")
	}
	if !strings.Contains(sentSystem, "USER-SUBMITTED CODE") {
		t.Errorf("system prompt missing user-code warning")
	}

	// The user message should carry the diff and file list
	sentUser := adapter.seenRequest.Messages[1].Content
	if !strings.Contains(sentUser, "main.go") {
		t.Errorf("user message missing file path")
	}
	if !strings.Contains(sentUser, "+new") {
		t.Errorf("user message missing diff content")
	}
}

func TestExecute_SkipIfNoMatch(t *testing.T) {
	adapter := &stubAdapter{}
	runtime, provider := buildTestRuntime(adapter, "openai")
	agent := buildAgent(provider, types.AgentConfig{
		FilePatterns:  []string{"**/*.ts"},
		SkipIfNoMatch: true,
	})

	result, err := runtime.Execute(context.Background(), Input{
		Session: types.ReviewSession{ID: "s", HeadSHA: "aabbccdd"},
		Agent:   agent,
		Diff:    "...",
		FileTree: []types.FileEntry{
			{Path: "main.go", Status: "modified"},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !result.Skipped {
		t.Errorf("agent should be skipped when no files match")
	}
	if result.Report.Verdict != types.VerdictNotApplicable {
		t.Errorf("skipped report verdict: got %q, want not_applicable", result.Report.Verdict)
	}
	if adapter.calls != 0 {
		t.Errorf("adapter should not be called when skipped, got %d calls", adapter.calls)
	}
}

func TestExecute_SkipIfNoMatch_FilePatternMatches(t *testing.T) {
	adapter := &stubAdapter{}
	runtime, provider := buildTestRuntime(adapter, "openai")
	agent := buildAgent(provider, types.AgentConfig{
		FilePatterns:  []string{"**/*.go"},
		SkipIfNoMatch: true,
	})

	result, err := runtime.Execute(context.Background(), Input{
		Session: types.ReviewSession{ID: "s", HeadSHA: "aabbccdd"},
		Agent:   agent,
		FileTree: []types.FileEntry{
			{Path: "cmd/server/main.go", Status: "modified"},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Skipped {
		t.Errorf("agent should not be skipped when a file matches")
	}
	if adapter.calls != 1 {
		t.Errorf("adapter should be called once, got %d", adapter.calls)
	}
}

func TestExecute_SkipIfNoMatch_FalseMeansAlwaysRun(t *testing.T) {
	adapter := &stubAdapter{}
	runtime, provider := buildTestRuntime(adapter, "openai")
	agent := buildAgent(provider, types.AgentConfig{
		FilePatterns:  []string{"**/*.ts"},
		SkipIfNoMatch: false, // explicit
	})

	result, err := runtime.Execute(context.Background(), Input{
		Session: types.ReviewSession{ID: "s", HeadSHA: "aabbccdd"},
		Agent:   agent,
		FileTree: []types.FileEntry{
			{Path: "main.go", Status: "modified"},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.Skipped {
		t.Errorf("SkipIfNoMatch=false should run regardless of patterns")
	}
	if adapter.calls != 1 {
		t.Errorf("adapter calls: got %d, want 1", adapter.calls)
	}
}

func TestExecute_LLMErrorIsWrapped(t *testing.T) {
	adapter := &stubAdapter{err: errors.New("upstream 500")}
	runtime, provider := buildTestRuntime(adapter, "openai")
	agent := buildAgent(provider, types.AgentConfig{})

	_, err := runtime.Execute(context.Background(), Input{
		Session:  types.ReviewSession{ID: "s", HeadSHA: "aabbccdd"},
		Agent:    agent,
		Diff:     "d",
		FileTree: []types.FileEntry{{Path: "foo.go", Status: "modified"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "security") {
		t.Errorf("error should mention skill slug: %v", err)
	}
	if !strings.Contains(err.Error(), "upstream 500") {
		t.Errorf("error should wrap upstream error: %v", err)
	}
}

func TestExecute_ModelOverrideIsUsed(t *testing.T) {
	adapter := &stubAdapter{}
	runtime, provider := buildTestRuntime(adapter, "anthropic")
	agent := buildAgent(provider, types.AgentConfig{})
	override := "claude-opus-4-6"
	agent.ModelOverride = &override

	_, err := runtime.Execute(context.Background(), Input{
		Session:  types.ReviewSession{ID: "s", HeadSHA: "aabbccdd"},
		Agent:    agent,
		FileTree: []types.FileEntry{{Path: "foo.go", Status: "modified"}},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if adapter.seenRequest.Model != "claude-opus-4-6" {
		t.Errorf("override not used, got model %q", adapter.seenRequest.Model)
	}
}

func TestExecute_ContextDocumentsAppended(t *testing.T) {
	adapter := &stubAdapter{}
	runtime, provider := buildTestRuntime(adapter, "openai")
	agent := buildAgent(provider, types.AgentConfig{})
	agent.Contexts = []types.ContextDocument{
		{
			ID:      "c1",
			Name:    "Security non-negotiables",
			Content: "Never log user tokens at info level.",
			DocType: "non-negotiable",
		},
	}

	_, err := runtime.Execute(context.Background(), Input{
		Session:  types.ReviewSession{ID: "s", HeadSHA: "aabbccdd"},
		Agent:    agent,
		FileTree: []types.FileEntry{{Path: "foo.go", Status: "modified"}},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	sentSystem := adapter.seenRequest.Messages[0].Content
	if !strings.Contains(sentSystem, "NON-NEGOTIABLE: Security non-negotiables") {
		t.Errorf("context document not injected into system prompt: %q", sentSystem)
	}
	if !strings.Contains(sentSystem, "Never log user tokens") {
		t.Errorf("context content missing from system prompt")
	}
}

