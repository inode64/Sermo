package checks

import (
	"sermo/internal/cfgval"
	"sermo/internal/severity"
)

// AnalyzeSeverityOK grades an output-analysis match as benign: an `ok` rule
// whitelists the line it matches. Every other grade is a severity level
// (internal/severity), so an analyze rule and a check's own `severity:` are
// spelled the same way. A check itself never declares ok: a check with nothing
// to say does not fail in the first place.
const AnalyzeSeverityOK = "ok"

// AnalyzeSeveritySummary names the grades an analyze rule accepts, for error
// text.
const AnalyzeSeveritySummary = AnalyzeSeverityOK + ", " + severity.Summary

// IsAnalyzeSeverity reports whether s is a grade an analyze rule may assign.
func IsAnalyzeSeverity(s string) bool {
	if s == AnalyzeSeverityOK {
		return true
	}
	_, ok := severity.Parse(s)
	return ok
}

// DeclaredSeverity is the narrowest valid `severity:` the trees declare, or
// unset when none does. Trees are given broadest first — watch entry, check
// block, metric block — so a `net` watch can call its error counter an
// advisory while its link state stays an outage. The distinction between unset
// and Error matters to the check: one that receives no declaration may grade
// its own finding (a SMART predicate under a PASSED verdict is an advisory),
// while a declaration always wins.
func DeclaredSeverity(trees ...map[string]any) severity.Level {
	var declared severity.Level
	for _, tree := range trees {
		if level, ok := severity.Parse(cfgval.AsString(tree[CheckKeySeverity])); ok {
			declared = level
		}
	}
	return declared
}
