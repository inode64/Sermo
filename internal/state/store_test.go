package state

import (
	"context"
	"path/filepath"
	"strings"
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

func TestStoreOperationSettlingRoundTrip(t *testing.T) {
	s := openTemp(t)
	s.now = func() time.Time { return time.Date(2026, 6, 7, 9, 0, 0, 0, time.UTC) }

	if err := s.SetOperationSettling("web", "restart", OperationSettlingRunning, SourceCLI); err != nil {
		t.Fatalf("SetOperationSettling: %v", err)
	}
	rec, found, err := s.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("OperationSettling: found=%v err=%v", found, err)
	}
	if rec.Action != "restart" || rec.Phase != OperationSettlingRunning || rec.Source != SourceCLI || !rec.UpdatedAt.Equal(s.now()) {
		t.Fatalf("record = %+v", rec)
	}

	s.now = func() time.Time { return time.Date(2026, 6, 7, 9, 1, 0, 0, time.UTC) }
	if err := s.SetOperationSettling("web", "restart", OperationSettlingSettling, SourceWeb); err != nil {
		t.Fatalf("SetOperationSettling update: %v", err)
	}
	rec, found, err = s.OperationSettling("web")
	if err != nil || !found {
		t.Fatalf("OperationSettling after update: found=%v err=%v", found, err)
	}
	if rec.Phase != OperationSettlingSettling || rec.Source != SourceWeb || !rec.UpdatedAt.Equal(s.now()) {
		t.Fatalf("updated record = %+v", rec)
	}

	if err := s.ClearOperationSettling("web"); err != nil {
		t.Fatalf("ClearOperationSettling: %v", err)
	}
	if _, found, err = s.OperationSettling("web"); err != nil || found {
		t.Fatalf("after clear found=%v err=%v", found, err)
	}
}

func TestStoreCheckSnapshotsPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), Filename)
	at := time.Date(2026, 7, 9, 11, 30, 0, 0, time.UTC)

	first, err := OpenContext(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := first.SetServiceCheckSnapshots("web", map[string]CheckSnapshotRecord{
		"http": {
			CheckType: "http", OK: true, Message: "status 200", Data: map[string]any{"status": float64(200)}, Ran: true, At: at,
		},
		"stale": {
			OK: false, Message: "old", Ran: true, At: at.Add(-time.Minute),
		},
	}); err != nil {
		t.Fatalf("SetServiceCheckSnapshots initial: %v", err)
	}
	if err := first.SetServiceCheckSnapshots("web", map[string]CheckSnapshotRecord{
		"http": {
			CheckType: "http", OK: true, Message: "status 200", Data: map[string]any{"status": float64(200)}, Ran: true, At: at,
		},
	}); err != nil {
		t.Fatalf("SetServiceCheckSnapshots replace: %v", err)
	}
	if err := first.SetWatchCheckSnapshot("clock", "result", CheckSnapshotRecord{
		CheckType: "clock", OK: false, Message: "offset 1200ms",
		Data: map[string]any{"offset_ms": float64(1200)}, Ran: true, At: at,
	}); err != nil {
		t.Fatalf("SetWatchCheckSnapshot: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := OpenContext(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()

	serviceSnapshots, err := second.ServiceCheckSnapshots()
	if err != nil {
		t.Fatalf("ServiceCheckSnapshots: %v", err)
	}
	service := serviceSnapshots["web"]
	if len(service) != 1 {
		t.Fatalf("service snapshots = %+v, want only current row", service)
	}
	if got := service["http"]; got.CheckType != "http" || !got.OK || got.Message != "status 200" || got.Data["status"] != float64(200) || !got.Ran || !got.At.Equal(at) {
		t.Fatalf("service snapshot did not round-trip: %+v", got)
	}

	watchSnapshots, err := second.WatchCheckSnapshots()
	if err != nil {
		t.Fatalf("WatchCheckSnapshots: %v", err)
	}
	got := watchSnapshots["clock"]["result"]
	if got.CheckType != "clock" || got.OK || got.Message != "offset 1200ms" || got.Data["offset_ms"] != float64(1200) || !got.At.Equal(at) {
		t.Fatalf("watch snapshot did not round-trip: %+v", got)
	}
}

func TestStoreEventAppDimensionRoundTrip(t *testing.T) {
	s := openTemp(t)
	if _, err := s.RecordEvent(EventRecord{Service: "web", Kind: "action", Message: "restart"}); err != nil {
		t.Fatalf("RecordEvent service: %v", err)
	}
	if _, err := s.RecordEvent(EventRecord{App: "salt-minion", Kind: "firing", Message: "error: exit 1", Output: "stderr:\nImportError: no module"}); err != nil {
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

	first, err := OpenContext(context.Background(), path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	writePersistentStoreState(t, first, t0)
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	second, err := OpenContext(context.Background(), path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer second.Close()
	assertPersistentStoreState(t, second, t0)
}

func writePersistentStoreState(t *testing.T, store *Store, at time.Time) {
	t.Helper()
	if err := store.SetActive("db", false, SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	if err := store.SetRemediationState("db", RemediationRecord{
		LastActionAt:   at,
		RecentActions:  []time.Time{at.Add(-time.Minute), at},
		CurrentBackoff: 2 * time.Minute,
	}); err != nil {
		t.Fatalf("SetRemediationState: %v", err)
	}
	if err := store.SetRuleWindowStates("db", map[string]RuleWindowRecord{
		"restart-if-down": {
			Consecutive: 2,
			History:     []bool{true, false, true},
			TrueSince:   at.Add(-5 * time.Minute),
			TimedHistory: []RuleWindowSample{
				{At: at.Add(-4 * time.Minute), Match: true},
				{At: at.Add(-2 * time.Minute), Match: false},
			},
		},
	}); err != nil {
		t.Fatalf("SetRuleWindowStates: %v", err)
	}
	if err := store.SetWatchRuntimeState("storage-root", "metric:free", WatchRuntimeRecord{
		Firing:       true,
		LastNotifyAt: at.Add(-time.Minute),
		Window: RuleWindowRecord{
			Consecutive: 2,
			History:     []bool{true, false, true},
			TrueSince:   at.Add(-5 * time.Minute),
			TimedHistory: []RuleWindowSample{
				{At: at.Add(-4 * time.Minute), Match: true},
			},
		},
		Policy: RemediationRecord{
			LastActionAt:   at.Add(-2 * time.Minute),
			RecentActions:  []time.Time{at.Add(-2 * time.Minute)},
			CurrentBackoff: 3 * time.Minute,
		},
	}); err != nil {
		t.Fatalf("SetWatchRuntimeState: %v", err)
	}
	if err := store.SetOperationSettling("db", "restart", OperationSettlingSettling, SourceDaemon); err != nil {
		t.Fatalf("SetOperationSettling: %v", err)
	}
}

func assertPersistentStoreState(t *testing.T, store *Store, at time.Time) {
	t.Helper()
	assertPersistedActiveState(t, store)
	assertPersistedRemediationState(t, store, at)
	assertPersistedRuleWindowState(t, store, at)
	assertPersistedWatchRuntimeState(t, store, at)
	assertPersistedOperationSettling(t, store)
}

func assertPersistedActiveState(t *testing.T, store *Store) {
	t.Helper()
	active, found, err := store.Active("db")
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if !found || active {
		t.Errorf("state did not persist across reopen: found=%v active=%v", found, active)
	}
}

func assertPersistedRemediationState(t *testing.T, store *Store, at time.Time) {
	t.Helper()
	rem, found, err := store.RemediationState("db")
	if err != nil || !found {
		t.Fatalf("RemediationState after reopen: found=%v err=%v", found, err)
	}
	if !rem.LastActionAt.Equal(at) || rem.CurrentBackoff != 2*time.Minute || len(rem.RecentActions) != 2 {
		t.Fatalf("remediation state = %+v", rem)
	}
}

func assertPersistedRuleWindowState(t *testing.T, store *Store, at time.Time) {
	t.Helper()
	windows, err := store.RuleWindowStates("db")
	if err != nil {
		t.Fatalf("RuleWindowStates after reopen: %v", err)
	}
	if rec, ok := windows["restart-if-down"]; !ok || rec.Consecutive != 2 || len(rec.History) != 3 || !rec.History[2] {
		t.Fatalf("rule window state = %+v", windows)
	} else if !rec.TrueSince.Equal(at.Add(-5*time.Minute)) || len(rec.TimedHistory) != 2 || rec.TimedHistory[0].Match != true || !rec.TimedHistory[1].At.Equal(at.Add(-2*time.Minute)) {
		t.Fatalf("duration rule window state = %+v", rec)
	}
}

func assertPersistedWatchRuntimeState(t *testing.T, store *Store, at time.Time) {
	t.Helper()
	watch, found, err := store.WatchRuntimeState("storage-root", "metric:free")
	if err != nil || !found {
		t.Fatalf("WatchRuntimeState after reopen: found=%v err=%v", found, err)
	}
	if !watch.Firing || !watch.LastNotifyAt.Equal(at.Add(-time.Minute)) ||
		watch.Window.Consecutive != 2 || len(watch.Window.History) != 3 ||
		!watch.Window.TrueSince.Equal(at.Add(-5*time.Minute)) ||
		len(watch.Window.TimedHistory) != 1 ||
		!watch.Policy.LastActionAt.Equal(at.Add(-2*time.Minute)) ||
		len(watch.Policy.RecentActions) != 1 || watch.Policy.CurrentBackoff != 3*time.Minute {
		t.Fatalf("watch runtime state = %+v", watch)
	}
}

func assertPersistedOperationSettling(t *testing.T, store *Store) {
	t.Helper()
	op, found, err := store.OperationSettling("db")
	if err != nil || !found {
		t.Fatalf("OperationSettling after reopen: found=%v err=%v", found, err)
	}
	if op.Action != "restart" || op.Phase != OperationSettlingSettling || op.Source != SourceDaemon {
		t.Fatalf("operation settling state = %+v", op)
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
		if err := s.RecordProcessUptime("web", at, at); err != nil {
			t.Fatalf("RecordProcessUptime(%s): %v", at, err)
		}
		if _, err := s.RecordEvent(EventRecord{At: at, Service: "web", Kind: "action", Message: "restart"}); err != nil {
			t.Fatalf("RecordEvent(%s): %v", at, err)
		}
	}

	result, err := s.PruneHistory(recent)
	if err != nil {
		t.Fatalf("PruneHistory: %v", err)
	}
	if result.SLA != 2 || result.Measurements != 1 || result.Metrics != 1 || result.DaemonMetrics != 1 || result.ServiceMetrics != 1 || result.ProcessUptime != 1 || result.Events != 1 || result.Rows != 8 {
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

func TestRecordAggregatesPreserveErrorContext(t *testing.T) {
	s, err := OpenContext(context.Background(), filepath.Join(t.TempDir(), Filename))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	at := time.Unix(0, 0)
	for name, test := range map[string]struct {
		record func() error
		want   string
	}{
		"measurement": {func() error { return s.RecordMeasurement("web", "http", 1, at) }, "record measurement for web/http:"},
		"metric":      {func() error { return s.RecordMetric("web", "http", "latency", 1, at) }, "record metric for web/http/latency:"},
		"daemon":      {func() error { return s.RecordDaemonMetric("cpu", 1, at) }, "record daemon metric cpu:"},
		"service":     {func() error { return s.RecordServiceMetric("web", "cpu", 1, at) }, "record service metric for web/cpu:"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := test.record(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("record error = %v, want context %q", err, test.want)
			}
		})
	}
}

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := OpenContext(context.Background(), filepath.Join(t.TempDir(), Filename))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestSetRemediationStatePersistsPartialRecords(t *testing.T) {
	s, err := OpenContext(context.Background(), filepath.Join(t.TempDir(), Filename))
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
	s, err := OpenContext(context.Background(), path)
	if err != nil {
		t.Fatalf("Open must create the parent dir, got: %v", err)
	}
	s.Close()
}
