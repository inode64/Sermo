package checks

import (
	"context"
	"testing"
	"time"
)

type summaryTestCheck struct {
	result Result
}

func (c summaryTestCheck) Name() string { return c.result.Check }

func (c summaryTestCheck) Run(context.Context) Result { return c.result }

func TestSummaryCheckFormatsResultAndConfigurationValues(t *testing.T) {
	check := withSummary(summaryTestCheck{result: Result{
		Check: "geoip",
		OK:    true,
		Data: map[string]any{
			DataKeyValue:       481 * time.Hour,
			DataKeyNumberFiles: 12345,
			DataKeyResult:      "unused",
		},
	}}, map[string]any{
		CheckKeySummary:   "GeoIP ${value} is older than ${older_than} in ${number_files} files (${check.paths})",
		CheckKeyOlderThan: "480h",
		CheckKeyPaths:     []string{"/usr/share/GeoIP"},
	})

	result := check.Run(context.Background())
	const want = "GeoIP 2w6d1h is older than 2w6d in 12.345 files (/usr/share/GeoIP)"
	if result.Message != want {
		t.Fatalf("summary = %q, want %q", result.Message, want)
	}
}

func TestSummaryCheckKeepsUnknownReferencesVisible(t *testing.T) {
	result := ApplySummary("value=${value}, missing=${result.missing}", nil, Result{Data: map[string]any{DataKeyValue: 42}})
	if result.Message != "value=42, missing=${result.missing}" {
		t.Fatalf("summary = %q", result.Message)
	}
}
