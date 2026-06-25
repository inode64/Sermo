package checks

import "testing"

func mustAnalyzer(t *testing.T, rules []any) *outputAnalyzer {
	t.Helper()
	a, warn := parseAnalyzer(map[string]any{"rules": rules})
	if warn != "" {
		t.Fatalf("parseAnalyzer: %s", warn)
	}
	return a
}

func rule(id, match, sev string, stream ...string) map[string]any {
	m := map[string]any{"id": id, "match": match, "severity": sev}
	if len(stream) > 0 {
		m["stream"] = stream[0]
	}
	return m
}

func TestAnalyzeMaxSeverityAndFirstMatch(t *testing.T) {
	a := mustAnalyzer(t, []any{
		rule("err", "(?i)BACK UP DATA NOW", "error"),
		rule("warn", "(?i)deprecated", "warning"),
	})
	sev, id, _ := a.Analyze("all fine\nfeature is deprecated\nBACK UP DATA NOW", "")
	if sev != SevError || id != "err" {
		t.Fatalf("sev=%v id=%q, want error/err", sev, id)
	}
}

func TestAnalyzeReportsFirstLineReachingMaxSeverity(t *testing.T) {
	// Two lines both reach error severity; the reported line must be the first
	// one to reach it (severity > sev, not >=, so a later equal match never wins).
	a := mustAnalyzer(t, []any{rule("err", "FAIL", "error")})
	sev, id, line := a.Analyze("FAIL first\nFAIL second", "")
	if sev != SevError || id != "err" || line != "FAIL first" {
		t.Fatalf("sev=%v id=%q line=%q, want error/err/\"FAIL first\"", sev, id, line)
	}
}

func TestAnalyzeOkWhitelistsLine(t *testing.T) {
	// An ok rule earlier in the list suppresses a later warning on the same line.
	a := mustAnalyzer(t, []any{
		rule("benign", "(?i)deprecated option ignored", "ok"),
		rule("warn", "(?i)deprecated", "warning"),
	})
	if sev, _, _ := a.Analyze("deprecated option ignored", ""); sev != SevOK {
		t.Fatalf("ok rule must whitelist the line, got %v", sev)
	}
	// But a different deprecated line is still a warning.
	if sev, _, _ := a.Analyze("X is deprecated", ""); sev != SevWarning {
		t.Fatalf("a non-whitelisted line must warn, got %v", sev)
	}
}

func TestAnalyzeStreamScoping(t *testing.T) {
	a := mustAnalyzer(t, []any{rule("e", "boom", "error", "stderr")})
	if sev, _, _ := a.Analyze("boom", ""); sev != SevOK {
		t.Fatalf("stderr-scoped rule must ignore stdout, got %v", sev)
	}
	if sev, _, _ := a.Analyze("", "boom"); sev != SevError {
		t.Fatalf("stderr-scoped rule must match stderr, got %v", sev)
	}
}

func TestParseAnalyzerErrors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		rules []any
		want  string
	}{
		{"bad-regex", []any{rule("x", "(", "warning")}, "invalid"},
		{"bad-severity", []any{rule("x", "y", "fatal")}, "severity"},
		{"bad-stream", []any{rule("x", "y", "warning", "syslog")}, "stream"},
		{"dup-id", []any{rule("x", "a", "warning"), rule("x", "b", "error")}, "duplicate"},
		{"missing-id", []any{map[string]any{"match": "y", "severity": "warning"}}, "id"},
	} {
		if _, warn := parseAnalyzer(map[string]any{"rules": tc.rules}); warn == "" {
			t.Errorf("%s: expected a warning containing %q", tc.name, tc.want)
		}
	}
}

func TestAnalyzerActiveRequiresRules(t *testing.T) {
	// A non-nil analyzer with zero rules is inert: Active needs len(rules) > 0,
	// not >= 0.
	if (&outputAnalyzer{}).Active() {
		t.Fatal("analyzer with no rules must not be Active")
	}
	if !mustAnalyzer(t, []any{rule("x", "y", "warning")}).Active() {
		t.Fatal("analyzer with a rule must be Active")
	}
}

func TestParseAnalyzerInertWhenAbsent(t *testing.T) {
	if a, warn := parseAnalyzer(nil); a != nil || warn != "" {
		t.Fatalf("nil analyze must be inert, got a=%v warn=%q", a, warn)
	}
	a, warn := parseAnalyzer(map[string]any{})
	if warn != "" || a.Active() {
		t.Fatalf("ruleless analyze must be inert, got a=%v warn=%q", a, warn)
	}
}
