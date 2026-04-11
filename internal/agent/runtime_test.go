package agent

import (
	"testing"

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
