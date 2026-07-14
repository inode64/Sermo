package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/metrics"
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

func TestFormatDisplayValueFormatsDecimalsAndThousands(t *testing.T) {
	if got, want := FormatDisplayValue(DataKeyValue, 12345.678), "12.345,68"; got != want {
		t.Fatalf("formatted number = %q, want %q", got, want)
	}
}

func TestFormatDisplayValueWithUnitFormatsBytes(t *testing.T) {
	tests := []struct {
		name  string
		value any
		unit  string
		want  string
	}{
		{name: "bytes", value: 2555904, unit: metrics.MetricUnitBytes, want: "2,44 MB"},
		{name: "threshold", value: "174159463", unit: metrics.MetricUnitBytes, want: "166,09 MB"},
		{name: "rate", value: 1048576, unit: metrics.MetricUnitBytesPerSecond, want: "1 MB/s"},
		{name: "percent", value: 73.5, unit: metrics.MetricUnitPercent, want: "73,5%"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatDisplayValueWithUnit(DataKeyValue, tt.value, tt.unit); got != tt.want {
				t.Fatalf("formatted value = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSummaryFormatsMetricValuesAndThresholdsWithTheirUnit(t *testing.T) {
	result := ApplySummary("current ${value}; threshold ${threshold}; configured ${check.value}", map[string]any{
		CheckKeyValue: "174159463",
	}, Result{Data: map[string]any{
		DataKeyValue:     2555904,
		DataKeyThreshold: "174159463",
		DataKeyUnit:      metrics.MetricUnitBytes,
	}})
	const want = "current 2,44 MB; threshold 166,09 MB; configured 166,09 MB"
	if result.Message != want {
		t.Fatalf("summary = %q, want %q", result.Message, want)
	}
}
