package app

import (
	"strings"
	"testing"

	"sermo/internal/config"
)

func TestFormatValidationIssues(t *testing.T) {
	issues := []config.Issue{
		{Msg: "first"},
		{Msg: "second"},
	}
	got := formatValidationIssues(issues)
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Fatalf("formatValidationIssues = %q, want both issues", got)
	}

	many := make([]config.Issue, 7)
	for i := range many {
		many[i] = config.Issue{Msg: "issue"}
	}
	got = formatValidationIssues(many)
	if !strings.Contains(got, "... and 2 more") {
		t.Fatalf("formatValidationIssues = %q, want truncation suffix", got)
	}
}
