package config

import (
	"fmt"
	"testing"
)

func TestVariableSubstitutionModes(t *testing.T) {
	tests := []struct {
		name       string
		substitute func(string, map[string]string, string) (string, []string)
		input      string
		vars       map[string]string
		want       string
		wantErr    string
	}{
		{
			name: "paths retain literal value", substitute: substituteVars,
			input: "${path}", vars: map[string]string{"path": "/run/service[1]"}, want: "/run/service[1]",
		},
		{
			name: "patterns quote value", substitute: substitutePatternVars,
			input: "^${path}$", vars: map[string]string{"path": "/run/service[1]"}, want: `^/run/service\[1\]$`,
		},
		{
			name: "unknown variable", substitute: substituteVars,
			input: "${missing}", vars: map[string]string{}, want: "${missing}",
			wantErr: "variable ${missing} used in variables.path but not defined",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, errs := test.substitute(test.input, test.vars, "variables.path")
			if got != test.want {
				t.Errorf("result = %q, want %q", got, test.want)
			}
			if test.wantErr == "" {
				if len(errs) != 0 {
					t.Errorf("errors = %v", errs)
				}
				return
			}
			if len(errs) != 1 || errs[0] != test.wantErr {
				t.Errorf("errors = %v, want %q", errs, test.wantErr)
			}
		})
	}
}

func TestExpandStringDefersCheckSummaryValues(t *testing.T) {
	var errs []string
	got := expandString("${value} / ${older_than} / ${check.value} / ${result.count}", nil, "watches.geoip.check.summary", &errs)
	if got != "${value} / ${older_than} / ${check.value} / ${result.count}" {
		t.Fatalf("expanded summary = %q", got)
	}
	if len(errs) != 0 {
		t.Fatalf("summary expansion errors = %v", errs)
	}
}

func TestValidateCheckSummaryRequiresString(t *testing.T) {
	var errs []string
	validateCheckSummary("checks.geoip", map[string]any{"summary": 42}, func(format string, args ...any) {
		errs = append(errs, fmt.Sprintf(format, args...))
	})
	if len(errs) != 1 || errs[0] != "checks.geoip.summary must be a string" {
		t.Fatalf("summary validation errors = %v", errs)
	}
}
