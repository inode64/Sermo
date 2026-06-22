package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreActiveDefaultsToNotFound(t *testing.T) {
	s := openTemp(t)

	active, found, err := s.Active("web")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if found {
		t.Errorf("a service with no recorded state must report found=false (got active=%v)", active)
	}
}

func TestStoreMonitorStateRoundTrip(t *testing.T) {
	s := openTemp(t)
	s.now = func() time.Time { return time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC) }

	if err := s.SetActive("web", false, SourceWeb); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	rec, found, err := s.MonitorState("web")
	if err != nil || !found {
		t.Fatalf("MonitorState: found=%v err=%v", found, err)
	}
	if rec.Active || rec.Source != SourceWeb || !rec.UpdatedAt.Equal(s.now()) {
		t.Fatalf("record = %+v", rec)
	}
}

func TestStoreSetActiveRoundTrip(t *testing.T) {
	s := openTemp(t)

	if err := s.SetActive("web", false, SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	active, found, err := s.Active("web")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !found || active {
		t.Errorf("want found=true active=false, got found=%v active=%v", found, active)
	}

	// Upsert flips the state without duplicating the row.
	if err := s.SetActive("web", true, SourceConfig); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if active, found, _ = s.Active("web"); !found || !active {
		t.Errorf("want found=true active=true after re-set, got found=%v active=%v", found, active)
	}
}

// State must survive a daemon restart/reboot — this is what `monitor: previous`
// relies on. Reopening the same file must preserve the recorded state.
func TestStorePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)
	t0 := time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC)

	first, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.SetActive("db", false, SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := first.SetRemediationState("db", RemediationRecord{
		LastActionAt:   t0,
		RecentActions:  []time.Time{t0.Add(-time.Minute), t0},
		CurrentBackoff: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("SetRemediationState: %v", err)
	}
	if err := first.SetRuleWindowStates("db", map[string]RuleWindowRecord{
		"restart-if-down": {
			Consecutive: 2,
			History:     []bool{true, false, true},
			TrueSince:   t0.Add(-5 * time.Minute),
			TimedHistory: []RuleWindowSample{
				{At: t0.Add(-4 * time.Minute), Match: true},
				{At: t0.Add(-2 * time.Minute), Match: false},
			},
		},
	}); err != nil {
		t.Fatalf("SetRuleWindowStates: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	active, found, err := second.Active("db")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !found || active {
		t.Errorf("state did not persist across reopen: found=%v active=%v", found, active)
	}
	rem, found, err := second.RemediationState("db")
	if err != nil || !found {
		t.Fatalf("RemediationState after reopen: found=%v err=%v", found, err)
	}
	if !rem.LastActionAt.Equal(t0) || rem.CurrentBackoff != 2*time.Minute || len(rem.RecentActions) != 2 {
		t.Fatalf("remediation state = %+v", rem)
	}
	windows, err := second.RuleWindowStates("db")
	if err != nil {
		t.Fatalf("RuleWindowStates after reopen: %v", err)
	}
	if rec, ok := windows["restart-if-down"]; !ok || rec.Consecutive != 2 || len(rec.History) != 3 || !rec.History[2] {
		t.Fatalf("rule window state = %+v", windows)
	} else if !rec.TrueSince.Equal(t0.Add(-5*time.Minute)) || len(rec.TimedHistory) != 2 || rec.TimedHistory[0].Match != true || !rec.TimedHistory[1].At.Equal(t0.Add(-2*time.Minute)) {
		t.Fatalf("duration rule window state = %+v", rec)
	}
}

func TestStoreRuleWindowReplaceRemovesStaleRules(t *testing.T) {
	s := openTemp(t)
	if err := s.SetRuleWindowStates("web", map[string]RuleWindowRecord{
		"old": {Consecutive: 2},
		"new": {History: []bool{true}},
	}); err != nil {
		t.Fatalf("SetRuleWindowStates initial: %v", err)
	}
	if err := s.SetRuleWindowStates("web", map[string]RuleWindowRecord{
		"new": {Consecutive: 1},
	}); err != nil {
		t.Fatalf("SetRuleWindowStates replace: %v", err)
	}
	windows, err := s.RuleWindowStates("web")
	if err != nil {
		t.Fatalf("RuleWindowStates: %v", err)
	}
	if _, ok := windows["old"]; ok {
		t.Fatalf("stale rule window was not removed: %+v", windows)
	}
	if windows["new"].Consecutive != 1 {
		t.Fatalf("new rule window = %+v", windows["new"])
	}
}

func TestPruneUnconfiguredControlStates(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

	for _, service := range []string{"web", "ghost"} {
		if err := s.SetActive(service, false, SourceCLI); err != nil {
			t.Fatalf("SetActive(%s): %v", service, err)
		}
		if err := s.RecordSLA(service, true, now); err != nil {
			t.Fatalf("RecordSLA(%s): %v", service, err)
		}
		if err := s.RecordCheckSLA(service, "http", true, now); err != nil {
			t.Fatalf("RecordCheckSLA(%s): %v", service, err)
		}
		if err := s.RecordMeasurement(service, "http", 10, now); err != nil {
			t.Fatalf("RecordMeasurement(%s): %v", service, err)
		}
		if err := s.RecordMetric(service, "http", "latency", 10, now); err != nil {
			t.Fatalf("RecordMetric(%s): %v", service, err)
		}
		if err := s.RecordServiceMetric(service, "cpu", 10, now); err != nil {
			t.Fatalf("RecordServiceMetric(%s): %v", service, err)
		}
		if err := s.SetRemediationState(service, RemediationRecord{LastActionAt: now, RecentActions: []time.Time{now}, CurrentBackoff: time.Minute}); err != nil {
			t.Fatalf("SetRemediationState(%s): %v", service, err)
		}
		if err := s.SetRuleWindowStates(service, map[string]RuleWindowRecord{"restart-if-down": {Consecutive: 1}}); err != nil {
			t.Fatalf("SetRuleWindowStates(%s): %v", service, err)
		}
	}

	result, err := s.PruneUnconfiguredControlStates([]string{"web"})
	if err != nil {
		t.Fatalf("PruneUnconfiguredControlStates: %v", err)
	}
	if result.Rows != 3 || len(result.Services) != 1 || result.Services[0] != "ghost" {
		t.Fatalf("result = %+v, want ghost with 3 rows", result)
	}

	if _, found, err := s.Active("ghost"); err != nil || found {
		t.Fatalf("ghost active: found=%v err=%v, want removed", found, err)
	}
	if _, found, err := s.Active("web"); err != nil || !found {
		t.Fatalf("web active: found=%v err=%v, want kept", found, err)
	}
	if _, found, err := s.RemediationState("ghost"); err != nil || found {
		t.Fatalf("ghost remediation: found=%v err=%v, want removed", found, err)
	}
	if _, found, err := s.RemediationState("web"); err != nil || !found {
		t.Fatalf("web remediation: found=%v err=%v, want kept", found, err)
	}
	if windows, err := s.RuleWindowStates("ghost"); err != nil || len(windows) != 0 {
		t.Fatalf("ghost rule windows = %+v err=%v, want removed", windows, err)
	}
	if windows, err := s.RuleWindowStates("web"); err != nil || len(windows) != 1 {
		t.Fatalf("web rule windows = %+v err=%v, want kept", windows, err)
	}
	if _, total, err := s.SLA("ghost", time.Hour, now.Add(time.Minute)); err != nil || total != 1 {
		t.Fatalf("ghost SLA total=%d err=%v, want preserved history", total, err)
	}
	if _, total, err := s.SLA("web", time.Hour, now.Add(time.Minute)); err != nil || total != 1 {
		t.Fatalf("web SLA total=%d err=%v, want 1", total, err)
	}
	if _, total, err := s.CheckSLA("ghost", "http", time.Hour, now.Add(time.Minute)); err != nil || total != 1 {
		t.Fatalf("ghost check SLA total=%d err=%v, want preserved history", total, err)
	}
	if _, total, err := s.CheckSLA("web", "http", time.Hour, now.Add(time.Minute)); err != nil || total != 1 {
		t.Fatalf("web check SLA total=%d err=%v, want 1", total, err)
	}
	if stat, err := s.MeasurementSummary("ghost", "http", time.Hour, now.Add(time.Minute)); err != nil || stat.Count != 1 {
		t.Fatalf("ghost measurement = %+v err=%v, want preserved history", stat, err)
	}
	if stat, err := s.MetricSummary("ghost", "http", "latency", time.Hour, now.Add(time.Minute)); err != nil || stat.Count != 1 {
		t.Fatalf("ghost metric = %+v err=%v, want preserved history", stat, err)
	}
	if stat, err := s.ServiceMetricSummary("ghost", "cpu", time.Hour, now.Add(time.Minute)); err != nil || stat.Count != 1 {
		t.Fatalf("ghost service metric = %+v err=%v, want preserved history", stat, err)
	}
}

func TestPruneHistory(t *testing.T) {
	s := openTemp(t)
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

	for _, at := range []time.Time{old, recent} {
		if err := s.RecordSLA("web", true, at); err != nil {
			t.Fatalf("RecordSLA(%s): %v", at, err)
		}
		if err := s.RecordCheckSLA("web", "http", true, at); err != nil {
			t.Fatalf("RecordCheckSLA(%s): %v", at, err)
		}
		if err := s.RecordMeasurement("web", "http", 10, at); err != nil {
			t.Fatalf("RecordMeasurement(%s): %v", at, err)
		}
		if err := s.RecordMetric("web", "http", "latency", 10, at); err != nil {
			t.Fatalf("RecordMetric(%s): %v", at, err)
		}
		if err := s.RecordDaemonMetric("cpu", 10, at); err != nil {
			t.Fatalf("RecordDaemonMetric(%s): %v", at, err)
		}
		if err := s.RecordServiceMetric("web", "cpu", 10, at); err != nil {
			t.Fatalf("RecordServiceMetric(%s): %v", at, err)
		}
		if err := s.RecordEvent(EventRecord{At: at, Service: "web", Kind: "action", Message: "restart"}); err != nil {
			t.Fatalf("RecordEvent(%s): %v", at, err)
		}
	}

	result, err := s.PruneHistory(recent)
	if err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if result.SLA != 2 || result.Measurements != 1 || result.Metrics != 1 || result.DaemonMetrics != 1 || result.ServiceMetrics != 1 || result.Events != 1 || result.Rows != 7 {
		t.Fatalf("PruneHistory = %+v, want one old bucket per history table", result)
	}

	now := recent.Add(time.Minute)
	if _, total, err := s.SLA("web", 2*time.Minute, now); err != nil || total != 1 {
		t.Fatalf("recent SLA total=%d err=%v, want 1", total, err)
	}
	if stat, err := s.MeasurementSummary("web", "http", 2*time.Minute, now); err != nil || stat.Count != 1 {
		t.Fatalf("recent measurement = %+v err=%v, want 1", stat, err)
	}
	if stat, err := s.ServiceMetricSummary("web", "cpu", 2*time.Minute, now); err != nil || stat.Count != 1 {
		t.Fatalf("recent service metric = %+v err=%v, want 1", stat, err)
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), Filename))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}
