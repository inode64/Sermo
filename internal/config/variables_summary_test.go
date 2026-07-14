package config

import (
	"fmt"
	"testing"
)

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
