package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
)

// availabilitySample is one recorded point of a watch's availability series.
type availabilitySample struct {
	up bool
	at time.Time
}

func recordingWatch(t *testing.T, checkType string, res checks.Result) (*Watch, *[]availabilitySample) {
	t.Helper()
	var got []availabilitySample
	w := &Watch{
		Name:      "net-eth0",
		CheckType: checkType,
		Check:     resultCheck{res: res},
		RecordAvailability: func(up bool, at time.Time) {
			got = append(got, availabilitySample{up: up, at: at})
		},
		Now: func() time.Time { return time.Date(2026, 8, 21, 20, 0, 0, 0, time.UTC) },
	}
	return w, &got
}

type resultCheck struct{ res checks.Result }

func (c resultCheck) Name() string                      { return "probe" }
func (c resultCheck) Run(context.Context) checks.Result { return c.res }

// TestWatchRecordsAvailabilityForReachabilityChecks is the point of the feature:
// a link-state watch keeps the same rolling availability a service does.
func TestWatchRecordsAvailabilityForReachabilityChecks(t *testing.T) {
	w, got := recordingWatch(t, checks.CheckTypeNet, checks.Result{
		Check: "probe", OK: true,
		Data: map[string]any{checks.DataKeyMetric: checks.NetMetricState},
	})
	w.RunCycle(t.Context())
	if len(*got) != 1 || !(*got)[0].up {
		t.Fatalf("samples = %+v, want one up sample", *got)
	}
}

// TestWatchRecordsNoAvailabilityForConditionChecks keeps a threshold out of the
// series: a disk crossing 90% used is a thing to look at, not an outage.
func TestWatchRecordsNoAvailabilityForConditionChecks(t *testing.T) {
	w, got := recordingWatch(t, checks.CheckTypeStorage, checks.Result{Check: "probe", OK: true})
	w.RunCycle(t.Context())
	if len(*got) != 0 {
		t.Fatalf("samples = %+v, want none for a condition check", *got)
	}
}

// TestWatchRecordsNoAvailabilityForOtherNetMetrics keeps a renegotiated speed
// and a CRC error out of the link's availability figure.
func TestWatchRecordsNoAvailabilityForOtherNetMetrics(t *testing.T) {
	for _, metric := range []string{checks.NetMetricSpeed, checks.NetMetricErrors, checks.NetMetricAddress} {
		w, got := recordingWatch(t, checks.CheckTypeNet, checks.Result{
			Check: "probe", OK: true, Data: map[string]any{checks.DataKeyMetric: metric},
		})
		w.RunCycle(t.Context())
		if len(*got) != 0 {
			t.Errorf("%s samples = %+v, want none", metric, *got)
		}
	}
}

// TestWatchRecordsNoAvailabilityWhenUnavailable is the rule the service side
// already holds: a check that could not run is missing data, never downtime.
func TestWatchRecordsNoAvailabilityWhenUnavailable(t *testing.T) {
	w, got := recordingWatch(t, checks.CheckTypeNet, checks.Result{
		Check: "probe", OK: true, Unavailable: true,
		Data: map[string]any{checks.DataKeyMetric: checks.NetMetricState},
	})
	w.RunCycle(t.Context())
	if len(*got) != 0 {
		t.Fatalf("samples = %+v, want none for an unavailable check", *got)
	}
}

// TestWatchRecordsNoAvailabilityForAdvisory keeps an advisory out of the series,
// matching the service rule: a warning is a thing to look at, not downtime.
func TestWatchRecordsNoAvailabilityForAdvisory(t *testing.T) {
	w, got := recordingWatch(t, checks.CheckTypeNet, checks.Result{
		Check: "probe", OK: false, Severity: checks.SeverityWarning,
		Data: map[string]any{checks.DataKeyMetric: checks.NetMetricState},
	})
	w.RunCycle(t.Context())
	if len(*got) != 0 {
		t.Fatalf("samples = %+v, want none for an advisory result", *got)
	}
}

// TestWatchRecordsDownWhenTheLinkIsDown pins the failing half, which is the half
// an availability figure exists to count.
func TestWatchRecordsDownWhenTheLinkIsDown(t *testing.T) {
	w, got := recordingWatch(t, checks.CheckTypeNet, checks.Result{
		Check: "probe", OK: false,
		Data: map[string]any{checks.DataKeyMetric: checks.NetMetricState},
	})
	w.RunCycle(t.Context())
	if len(*got) != 1 || (*got)[0].up {
		t.Fatalf("samples = %+v, want one down sample", *got)
	}
}
