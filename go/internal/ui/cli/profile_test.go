package cli

import (
	"strings"
	"testing"

	"github.com/rzbdz/newgate/go/internal/core/domain"
	"github.com/rzbdz/newgate/go/internal/core/resolve"
)

func TestTierOverviewUsesCandidateBoundaries(t *testing.T) {
	rows := []tierView{{name: "heavy", steps: []resolve.Step{
		{Binding: domain.Binding{Provider: "smt-deepseek", Model: "deepseek-flash"}},
		{Binding: domain.Binding{Provider: "smt-claude", Model: "claude-opus-5"}},
		{Binding: domain.Binding{Provider: "smt-gemini", Model: "gemini-3.1-pro-preview"}},
	}}}

	got := tierOverview(rows)
	for _, binding := range []string{
		"smt-deepseek/deepseek-flash",
		"smt-claude/claude-opus-5",
		"smt-gemini/gemini-3.1-pro-preview",
	} {
		if !strings.Contains(got, binding) {
			t.Fatalf("tierOverview() split or lost %q:\n%s", binding, got)
		}
	}
	if strings.Contains(got, "…") {
		t.Fatalf("tierOverview() hid candidates: %q", got)
	}
}
