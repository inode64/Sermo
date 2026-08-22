package checks

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// The check-type table in docs/rules.md advertises the configurable types, and
// its preamble promises the list is locked against the code. This test makes
// that promise true for the built-in registry: every registered type must have
// a table row, plus the watch-only forms the builder dispatches outside the
// registry. The reverse direction (a documented name with no implementation)
// is covered for connection protocols by conn's own docs test; type rows the
// registry does not know may be protocol rows, so they are not judged here.
func TestRulesDocTableCoversEveryBuiltinCheckType(t *testing.T) {
	data, err := os.ReadFile("../../docs/rules.md")
	if err != nil {
		t.Fatalf("read docs/rules.md: %v", err)
	}
	rowPattern := regexp.MustCompile(`(?m)^\|([^|]+)\|([^|]+)\|`)
	namePattern := regexp.MustCompile("`([a-z0-9_-]+)`")
	documented := map[string]string{}
	for _, row := range rowPattern.FindAllStringSubmatch(string(data), -1) {
		style := strings.TrimSpace(row[2])
		for _, name := range namePattern.FindAllStringSubmatch(row[1], -1) {
			documented[name[1]] = style
		}
	}
	// Watch-only forms: built by watch_build's dispatch, not builtinCheckSpecs.
	// process_policy fires on violations like the health family does.
	required := map[string]bool{CheckTypeProcessPolicy: true}
	for _, spec := range builtinCheckSpecs {
		required[spec.info.Name] = spec.info.DefaultReports == ReportsHealth
	}
	for name, health := range required {
		style, ok := documented[name]
		if !ok {
			t.Errorf("docs/rules.md check-type table is missing a row for %q", name)
			continue
		}
		want := "condition"
		if health {
			want = "health"
		}
		if style != want {
			t.Errorf("docs/rules.md style for %q = %q, want %q (from the registry)", name, style, want)
		}
	}
}
