package checks

import (
	"strings"
	"testing"

	"sermo/internal/severity"
)

func TestAnalyzeSeverityVocabulary(t *testing.T) {
	for _, grade := range []string{AnalyzeSeverityOK, "debug", "info", "warning", "error", "critical"} {
		if !IsAnalyzeSeverity(grade) {
			t.Errorf("IsAnalyzeSeverity(%q) = false, want true", grade)
		}
	}
	if IsAnalyzeSeverity("") || IsAnalyzeSeverity("urgent") {
		t.Error("IsAnalyzeSeverity accepted an empty or unknown grade")
	}
	// ok grades an analyze match; it is never a check's own severity.
	if _, ok := severity.Parse(AnalyzeSeverityOK); ok {
		t.Error("ok parsed as a check severity")
	}
}

func TestDeclaredSeverityNarrowestWins(t *testing.T) {
	watch := map[string]any{CheckKeySeverity: "warning"}
	check := map[string]any{CheckKeySeverity: "critical"}
	metric := map[string]any{CheckKeySeverity: "nope"}
	if got := DeclaredSeverity(watch, check, metric); got != severity.Critical {
		t.Fatalf("DeclaredSeverity = %q, want critical: an invalid narrower value inherits", got)
	}
	if got := DeclaredSeverity(map[string]any{}, nil); got.Valid() {
		t.Fatalf("DeclaredSeverity of an undeclared chain = %q, want unset", got)
	}
}

// Severity grades a failure; it never invents one and never cancels one. That is
// the invariant every health consumer leans on, so it is pinned per observation.
func TestResultAdvisoryNeverContradictsObservation(t *testing.T) {
	tests := []struct {
		name         string
		result       Result
		wantAdvisory bool
		wantCounts   bool
	}{
		{"failing warning", Result{Severity: severity.Warning}, true, false},
		{"failing error", Result{Severity: severity.Error}, false, true},
		{"failing info", Result{Severity: severity.Info}, true, false},
		{"failing debug", Result{Severity: severity.Debug}, true, false},
		{"failing critical", Result{Severity: severity.Critical}, false, true},
		{"undeclared failing", Result{}, false, true},
		{"unavailable warning", Result{Unavailable: true, Severity: severity.Warning}, true, false},
		// A healthy sample still counts toward health: it is the "up" side of the
		// SLA, and severity has nothing to grade.
		{"healthy warning is not a warning", Result{OK: true, Severity: severity.Warning}, false, true},
		{"skipped warning is not a warning", Result{Skipped: true, Severity: severity.Warning}, false, false},
		{"verdictless warning is not a warning", Result{Reports: ReportsValue, Severity: severity.Warning}, false, false},
		{"fired condition warning", Result{Condition: true, OK: true, Severity: severity.Warning}, true, false},
		{"quiet condition warning", Result{Condition: true, Severity: severity.Warning}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.Advisory(); got != tt.wantAdvisory {
				t.Errorf("Advisory() = %v, want %v", got, tt.wantAdvisory)
			}
			if got := tt.result.CountsTowardHealth(); got != tt.wantCounts {
				t.Errorf("CountsTowardHealth() = %v, want %v", got, tt.wantCounts)
			}
			// The safety invariant: severity may remove a result from aggregation,
			// never add it to the healthy set a guard trusts.
			if tt.wantAdvisory && tt.result.Healthy() {
				t.Error("Healthy() = true for an advisory: a guard could read a failure as safe")
			}
		})
	}
}

// The observation vocabulary is persisted and gate-checked on read, so severity
// must stay orthogonal to it rather than becoming a sixth state.
func TestSeverityIsNotAnObservationState(t *testing.T) {
	if ObservationState(severity.Warning.String()).Valid() {
		t.Fatal("warning is a valid ObservationState: severity must not widen the persisted vocabulary")
	}
	warning := Result{Condition: true, OK: true, Severity: severity.Warning}
	if got := warning.Observation(); got != ObservationFailing {
		t.Fatalf("Observation() = %q, want %q: severity must not change the verdict", got, ObservationFailing)
	}
}

// A severity the operator mistyped must stop the build rather than silently
// leaving the check graded an error.
func TestBuildRejectsUnknownSeverity(t *testing.T) {
	entry := map[string]any{
		CheckKeyType: CheckTypeLoad,
		"load1":      map[string]any{CheckKeyOp: ">", CheckKeyValue: 2},
	}
	if _, err := BuildInline("load", entry, Deps{}); err != nil {
		t.Fatalf("baseline build failed: %v", err)
	}

	entry[CheckKeySeverity] = "urgent"
	_, err := BuildInline("load", entry, Deps{})
	if err == nil {
		t.Fatal("build accepted severity \"urgent\", want a configuration failure")
	}
	if !strings.Contains(err.Error(), CheckKeySeverity) || !strings.Contains(err.Error(), severity.Summary) {
		t.Errorf("error = %q, want it to name the key and the accepted values", err)
	}

	entry[CheckKeySeverity] = AnalyzeSeverityOK
	if _, err := BuildInline("load", entry, Deps{}); err == nil {
		t.Fatal("build accepted severity \"ok\": ok grades an analyze match, not a check")
	}

	entry[CheckKeySeverity] = "critical"
	check, err := BuildInline("load", entry, Deps{})
	if err != nil {
		t.Fatalf("severity: critical rejected: %v", err)
	}
	if got := check.Run(t.Context()).Severity; got != severity.Critical {
		t.Errorf("result severity = %q, want critical: base must stamp it on every result", got)
	}
}
