package checks

import (
	"context"
	"errors"
	"testing"

	"sermo/internal/metrics"
)

func TestObservationFailureAvailability(t *testing.T) {
	tests := []struct {
		name            string
		check           Check
		wantUnavailable bool
	}{
		{
			name: "sampler error",
			check: memoryCheck{
				base:    base{name: "memory"},
				preds:   []levelPred{{field: fieldUsedPct, op: ">", value: 90}},
				sampler: func() (MemorySample, error) { return MemorySample{}, errors.New("read meminfo") },
			},
			wantUnavailable: true,
		},
		{
			name: "source missing",
			check: metricCheck{
				base: base{name: "metric"}, metric: "cpu", op: ">", value: "90",
			},
			wantUnavailable: true,
		},
		{
			name: "sample not ready",
			check: metricCheck{
				base: base{name: "metric"}, metric: "cpu", op: ">", value: "90",
				source: func(_, _ string) (metrics.Reading, bool) {
					return metrics.Reading{Ready: false}, true
				},
			},
			wantUnavailable: true,
		},
		{
			name: "valid predicate miss",
			check: memoryCheck{
				base:  base{name: "memory"},
				preds: []levelPred{{field: fieldUsedPct, op: ">", value: 90}},
				sampler: func() (MemorySample, error) {
					return MemorySample{TotalBytes: 100, AvailableBytes: 80}, nil
				},
			},
			wantUnavailable: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := test.check.Run(context.Background())
			if result.OK {
				t.Fatalf("result = %+v, want OK=false", result)
			}
			if result.Unavailable != test.wantUnavailable {
				t.Errorf("Unavailable = %v, want %v: %+v", result.Unavailable, test.wantUnavailable, result)
			}
		})
	}
}
