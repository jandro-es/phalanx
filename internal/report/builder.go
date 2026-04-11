// Package report builds composite Markdown review reports.
package report

import (
	"fmt"
	"strings"
	"time"

	"github.com/phalanx-ai/phalanx/internal/types"
)

var verdictIcons = map[types.Verdict]string{
	types.VerdictPass: "✅", types.VerdictWarn: "⚠️", types.VerdictFail: "🔴",
	types.VerdictError: "❌", types.VerdictNotApplicable: "⏭️",
}

var skillIcons = map[string]string{
	"accessibility": "♿", "security": "🔒", "complexity": "🧩",
	"architecture": "📐", "test-coverage": "🧪", "api-contract": "📡",
	"performance": "⚡", "documentation": "📖", "error-handling": "🚨",
	"code-style": "🎨",
}

// Builder creates composite reports.
type Builder struct {
	DashboardURL string
}

// BuildComposite assembles individual agent reports into a single report.
func (b *Builder) BuildComposite(session types.ReviewSession, reports []types.AgentReport) *types.CompositeReport {
	summaries := make([]types.AgentSummary, len(reports))
	for i, r := range reports {
		summaries[i] = types.AgentSummary{
			SkillSlug: r.SkillSlug,
			SkillName: formatName(r.SkillSlug),
			Verdict:   r.Verdict,
			LatencyMs: r.LatencyMs,
		}
	}

	overall := computeOverall(reports)
	md := b.render(session, reports, summaries, overall)

	return &types.CompositeReport{
		SessionID:      session.ID,
		Markdown:       md,
		OverallVerdict: overall,
		AgentSummaries: summaries,
		GeneratedAt:    time.Now(),
	}
}

func computeOverall(reports []types.AgentReport) types.Verdict {
	for _, r := range reports {
		if r.Verdict == types.VerdictFail || r.Verdict == types.VerdictError {
			return types.VerdictFail
		}
	}
	for _, r := range reports {
		if r.Verdict == types.VerdictWarn {
			return types.VerdictWarn
		}
	}
	return types.VerdictPass
}

func (b *Builder) render(session types.ReviewSession, reports []types.AgentReport,
	summaries []types.AgentSummary, overall types.Verdict) string {

	var s strings.Builder

	title := "Untitled"
	if session.PRTitle != nil {
		title = *session.PRTitle
	}

	fmt.Fprintf(&s, "# 🛡️ Phalanx Review — PR #%d\n\n", session.PRNumber)
	fmt.Fprintf(&s, "**%s** | Commit: `%s` | Reviewed: %s\n",
		title, shortSHA(session.HeadSHA), time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&s, "**Overall:** %s %s\n\n", verdictIcons[overall], strings.ToUpper(string(overall)))

	// Summary table
	s.WriteString("| Agent | Verdict | Duration |\n|---|---|---|\n")
	for _, sum := range summaries {
		icon := skillIcons[sum.SkillSlug]
		if icon == "" {
			icon = "🔍"
		}
		fmt.Fprintf(&s, "| %s %s | %s %s | %.1fs |\n",
			icon, sum.SkillName, verdictIcons[sum.Verdict],
			strings.ToUpper(string(sum.Verdict)), float64(sum.LatencyMs)/1000)
	}
	s.WriteString("\n")

	// Individual reports
	for _, r := range reports {
		icon := skillIcons[r.SkillSlug]
		if icon == "" {
			icon = "🔍"
		}
		name := formatName(r.SkillSlug)
		fmt.Fprintf(&s, "<details>\n<summary>%s %s — %s %s</summary>\n\n",
			icon, name, verdictIcons[r.Verdict], strings.ToUpper(string(r.Verdict)))
		fmt.Fprintf(&s, "> **Model:** %s (%s) | **Tokens:** %d→%d | **Duration:** %.1fs\n\n",
			r.ModelUsed, r.ProviderName, r.InputTokens, r.OutputTokens, float64(r.LatencyMs)/1000)
		s.WriteString(r.ReportMD)
		s.WriteString("\n\n</details>\n\n")
	}

	s.WriteString("---\n")
	fmt.Fprintf(&s, "*Phalanx | Session `%s`*\n", shortID(session.ID))

	return s.String()
}

func shortSHA(sha string) string {
	if len(sha) >= 7 {
		return sha[:7]
	}
	return sha
}

func shortID(id string) string {
	if len(id) >= 8 {
		return id[:8]
	}
	return id
}

func formatName(slug string) string {
	parts := strings.Split(slug, "-")
	for i, p := range parts {
		if len(p) > 0 {
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, " ")
}
