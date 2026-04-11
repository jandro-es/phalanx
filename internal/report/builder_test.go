package report

import (
	"strings"
	"testing"

	"github.com/phalanx-ai/phalanx/internal/types"
)

func sampleSession() types.ReviewSession {
	title := "Add widget API"
	return types.ReviewSession{
		ID:                 "11111111-2222-3333-4444-555555555555",
		PRNumber:           42,
		PRTitle:            &title,
		HeadSHA:            "abc1234567",
		RepositoryFullName: "acme/widget",
	}
}

func TestComputeOverall(t *testing.T) {
	cases := []struct {
		name    string
		reports []types.AgentReport
		want    types.Verdict
	}{
		{"all pass", []types.AgentReport{{Verdict: types.VerdictPass}, {Verdict: types.VerdictPass}}, types.VerdictPass},
		{"one warn", []types.AgentReport{{Verdict: types.VerdictPass}, {Verdict: types.VerdictWarn}}, types.VerdictWarn},
		{"one fail", []types.AgentReport{{Verdict: types.VerdictWarn}, {Verdict: types.VerdictFail}}, types.VerdictFail},
		{"error counts as fail", []types.AgentReport{{Verdict: types.VerdictError}}, types.VerdictFail},
		{"empty", nil, types.VerdictPass},
	}
	for _, c := range cases {
		got := computeOverall(c.reports)
		if got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestBuildComposite_MarkdownStructure(t *testing.T) {
	b := &Builder{}
	reports := []types.AgentReport{
		{
			SkillSlug: "security", SkillVersion: 1,
			ModelUsed: "claude-opus-4-6", ProviderName: "anthropic",
			InputTokens: 1000, OutputTokens: 200, LatencyMs: 1500,
			ReportMD: "## 🔒 Security Review\n\n**Verdict:** pass",
			Verdict:  types.VerdictPass,
		},
		{
			SkillSlug: "performance", SkillVersion: 1,
			ModelUsed: "gpt-4.1", ProviderName: "openai",
			InputTokens: 800, OutputTokens: 300, LatencyMs: 2000,
			ReportMD: "## ⚡ Performance\n\n**Verdict:** warn",
			Verdict:  types.VerdictWarn,
		},
	}

	comp := b.BuildComposite(sampleSession(), reports)
	if comp.OverallVerdict != types.VerdictWarn {
		t.Errorf("overall verdict: got %s, want warn", comp.OverallVerdict)
	}
	if len(comp.AgentSummaries) != 2 {
		t.Errorf("summaries: got %d, want 2", len(comp.AgentSummaries))
	}

	required := []string{
		"# 🛡️ Phalanx Review",
		"PR #42",
		"Add widget API",
		"Security", "Performance",
		"WARN",
	}
	for _, s := range required {
		if !strings.Contains(comp.Markdown, s) {
			t.Errorf("markdown missing %q", s)
		}
	}
}
