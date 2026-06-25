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

func TestStoreEventAppDimensionRoundTrip(t *testing.T) {
	s := openTemp(t)
	if err := s.RecordEvent(EventRecord{Service: "web", Kind: "action", Message: "restart"}); err != nil {
		t.Fatalf("RecordEvent service: %v", err)
	}
	if err := s.RecordEvent(EventRecord{App: "salt-minion", Kind: "firing", Message: "error: exit 1", Output: "stderr:\nImportError: no module"}); err != nil {
		t.Fatalf("RecordEvent app: %v", err)
	}
	recs, err := s.RecentEvents(0)
	if err != nil {
		t.Fatalf("RecentEvents: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 events, got %d", len(recs))
	}
	// newest first: the app event
	if recs[0].App != "salt-minion" || recs[0].Service != "" || recs[0].Message != "error: exit 1" {
		t.Fatalf("app event did not round-trip: %+v", recs[0])
	}
	if recs[0].Output != "stderr:\nImportError: no module" {
		t.Fatalf("event output did not round-trip: %q", recs[0].Output)
	}
	if recs[1].Output != "" {
		t.Fatalf("event without output must round-trip empty, got %q", recs[1].Output)
	}
	if recs[1].Service != "web" || recs[1].App != "" {
		t.Fatalf("service event did not round-trip: %+v", recs[1])
	}
}

func TestStorePanicDefaultsToOff(t *testing.T) {
	s := openTemp(t)

	rec, found, err := s.Panic()
	if err != nil {
		t.Fatalf("Panic: %v", err)
	}
	if found || rec.On {
		t.Errorf("a store with no panic row must report found=false, off (got %+v found=%v)", rec, found)
	}
}

func TestStorePanicRoundTrip(t *testing.T) {
	s := openTemp(t)
	s.now = func() time.Time { return time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC) }

	if err := s.SetPanic(true, SourceCLI); err != nil {
		t.Fatalf("SetPanic: %v", err)
	}
	rec, found, err := s.Panic()
	if err != nil || !found {
		t.Fatalf("Panic: found=%v err=%v", found, err)
	}
	if !rec.On || rec.Source != SourceCLI || !rec.UpdatedAt.Equal(s.now()) {
		t.Fatalf("record = %+v", rec)
	}

	// Upsert flips the flag without duplicating the row.
	if err := s.SetPanic(false, SourceWeb); err != nil {
		t.Fatalf("SetPanic: %v", err)
	}
	if rec, found, _ = s.Panic(); !found || rec.On || rec.Source != SourceWeb {
		t.Fatalf("after disable record = %+v found=%v", rec, found)
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

func TestSetRemediationStatePersistsPartialRecords(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), Filename))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()
	tm := time.Unix(1000, 0)
	// Only RecentActions set (no LastActionAt, no backoff): must persist, not delete.
	if err := s.SetRemediationState("a", RemediationRecord{RecentActions: []time.Time{tm}}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.RemediationState("a"); !found {
		t.Fatal("a record with only RecentActions must persist")
	}
	// Only CurrentBackoff set: must persist.
	if err := s.SetRemediationState("b", RemediationRecord{CurrentBackoff: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.RemediationState("b"); !found {
		t.Fatal("a record with only CurrentBackoff must persist")
	}
	// A fully-empty record deletes the row.
	if err := s.SetRemediationState("a", RemediationRecord{}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.RemediationState("a"); found {
		t.Fatal("an empty record must delete the row")
	}
}

func TestOpenCreatesParentDir(t *testing.T) {
	// Open must create a missing parent directory for the DB path.
	path := filepath.Join(t.TempDir(), "nested", "deeper", Filename)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open must create the parent dir, got: %v", err)
	}
	s.Close()
}
