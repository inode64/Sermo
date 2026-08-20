package checks

import (
	"strings"
	"testing"
)

func TestResolveSeverity(t *testing.T) {
	tests := []struct {
		name     string
		declared string
		fallback string
		want     string
	}{
		{"undeclared is an error", "", "", SeverityError},
		{"declared wins", SeverityWarning, SeverityError, SeverityWarning},
		{"empty inherits", "", SeverityWarning, SeverityWarning},
		{"an error declaration overrules an inherited warning", SeverityError, SeverityWarning, SeverityError},
		// A value that never passed validation must not silently demote a check.
		{"garbage inherits", "nope", SeverityWarning, SeverityWarning},
		{"garbage with no fallback is an error", "nope", "", SeverityError},
		// `ok` grades an analyze match; a check is either an error or a warning.
		{"ok is not a check severity", SeverityOK, "", SeverityError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveSeverity(tt.declared, tt.fallback); got != tt.want {
				t.Fatalf("ResolveSeverity(%q, %q) = %q, want %q", tt.declared, tt.fallback, got, tt.want)
			}
		})
	}
	if IsCheckSeverity(SeverityOK) {
		t.Error("IsCheckSeverity(ok) = true, want false: only error and warning may be declared on a check")
	}
	if got := CheckSeverities(); len(got) != 2 || got[0] != SeverityError || got[1] != SeverityWarning {
		t.Errorf("CheckSeverities() = %v, want [error warning]", got)
	}
}

// Severity grades a failure; it never invents one and never cancels one. That is
// the invariant every health consumer leans on, so it is pinned per observation.
func TestResultWarningNeverContradictsObservation(t *testing.T) {
	tests := []struct {
		name        string
		result      Result
		wantWarning bool
		wantCounts  bool
	}{
		{"failing warning", Result{Severity: SeverityWarning}, true, false},
		{"failing error", Result{Severity: SeverityError}, false, true},
		{"undeclared failing", Result{}, false, true},
		{"unavailable warning", Result{Unavailable: true, Severity: SeverityWarning}, true, false},
		// A healthy sample still counts toward health: it is the "up" side of the
		// SLA, and severity has nothing to grade.
		{"healthy warning is not a warning", Result{OK: true, Severity: SeverityWarning}, false, true},
		{"skipped warning is not a warning", Result{Skipped: true, Severity: SeverityWarning}, false, false},
		{"verdictless warning is not a warning", Result{Reports: ReportsValue, Severity: SeverityWarning}, false, false},
		{"fired condition warning", Result{Condition: true, OK: true, Severity: SeverityWarning}, true, false},
		{"quiet condition warning", Result{Condition: true, Severity: SeverityWarning}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.result.Warning(); got != tt.wantWarning {
				t.Errorf("Warning() = %v, want %v", got, tt.wantWarning)
			}
			if got := tt.result.CountsTowardHealth(); got != tt.wantCounts {
				t.Errorf("CountsTowardHealth() = %v, want %v", got, tt.wantCounts)
			}
			// The safety invariant: severity may remove a result from aggregation,
			// never add it to the healthy set a guard trusts.
			if tt.wantWarning && tt.result.Healthy() {
				t.Error("Healthy() = true for a warning: a guard could read a failure as safe")
			}
		})
	}
}

// The observation vocabulary is persisted and gate-checked on read, so severity
// must stay orthogonal to it rather than becoming a sixth state.
func TestSeverityIsNotAnObservationState(t *testing.T) {
	if ObservationState(SeverityWarning).Valid() {
		t.Fatal("warning is a valid ObservationState: severity must not widen the persisted vocabulary")
	}
	warning := Result{Condition: true, OK: true, Severity: SeverityWarning}
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
	if !strings.Contains(err.Error(), CheckKeySeverity) || !strings.Contains(err.Error(), SeverityWarning) {
		t.Errorf("error = %q, want it to name the key and the accepted values", err)
	}

	entry[CheckKeySeverity] = SeverityWarning
	check, err := BuildInline("load", entry, Deps{})
	if err != nil {
		t.Fatalf("severity: warning rejected: %v", err)
	}
	if got := check.Run(t.Context()).Severity; got != SeverityWarning {
		t.Errorf("result severity = %q, want warning: base must stamp it on every result", got)
	}
}
