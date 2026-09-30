package state

import (
	"context"
	"database/sql"
	"path/filepath"
	"reflect"
	"sermo/internal/rules"
	"testing"
	"time"
)

// TestStateColumnMigrationsHealAnOldDatabase pins the additive migrations: a
// database whose cache/control tables predate their newest columns opens
// cleanly and accepts writes, instead of failing every persist forever.
func TestStateColumnMigrationsHealAnOldDatabase(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")

	// Build the pre-observation shape by hand: the first shipped columns only.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE service_check_snapshot (
			service TEXT NOT NULL, check_name TEXT NOT NULL,
			ok INTEGER NOT NULL, condition INTEGER NOT NULL,
			optional INTEGER NOT NULL, skipped INTEGER NOT NULL,
			message TEXT NOT NULL, data TEXT NOT NULL,
			ran INTEGER NOT NULL, at INTEGER NOT NULL,
			PRIMARY KEY (service, check_name));`,
		`INSERT INTO service_check_snapshot VALUES ('web','http',1,0,0,0,'ok','{}',1,0);`,
		`CREATE TABLE watch_runtime_state (
			watch TEXT NOT NULL, slot TEXT NOT NULL,
			firing INTEGER NOT NULL DEFAULT 0,
			last_notify_at INTEGER NOT NULL DEFAULT 0,
			consecutive INTEGER NOT NULL DEFAULT 0,
			history TEXT NOT NULL DEFAULT '[]',
			true_since INTEGER NOT NULL DEFAULT 0,
			timed_history TEXT NOT NULL DEFAULT '[]',
			last_action_at INTEGER NOT NULL DEFAULT 0,
			recent_actions TEXT NOT NULL DEFAULT '[]',
			current_backoff_ns INTEGER NOT NULL DEFAULT 0,
			clear_since INTEGER NOT NULL DEFAULT 0,
			clear_consecutive INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (watch, slot));`,
		`CREATE TABLE service_restart_notice (
			service TEXT PRIMARY KEY, pid INTEGER NOT NULL, started_at TEXT NOT NULL);`,
		`INSERT INTO service_restart_notice VALUES ('web', 42, '2026-06-07T09:00:00Z');`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenContextWith(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("open over an old schema: %v", err)
	}
	defer func() { _ = s.Close() }()

	if err := s.SetServiceCheckSnapshots("web", map[string]CheckSnapshotRecord{
		"http": {CheckType: "http", Observation: "healthy", OK: true,
			Message: "ok", Ran: true, Severity: "warning"},
	}); err != nil {
		t.Fatalf("persist into the migrated table: %v", err)
	}
	snapshots, err := s.ServiceCheckSnapshots()
	if err != nil {
		t.Fatalf("read the migrated table: %v", err)
	}
	if got := snapshots["web"]["http"].Severity; got != "warning" {
		t.Fatalf("severity after migration = %q, want warning", got)
	}
	if err := s.SetWatchRuntimeState("storage-root", "result", WatchRuntimeRecord{
		Unavailable: true,
	}); err != nil {
		t.Fatalf("persist into the migrated watch runtime table: %v", err)
	}
	if got, found, err := s.ServiceRestartNotice("web"); err != nil || !found || got.PID != 42 || got.StartTicks != 0 {
		t.Fatalf("legacy restart notice = %+v, found=%v, err=%v; want pid 42 without start ticks", got, found, err)
	}
}

func TestRuleWindowMigrationPreservesProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE rule_window_state (
		service TEXT NOT NULL, rule_name TEXT NOT NULL,
		consecutive INTEGER NOT NULL DEFAULT 0,
		history TEXT NOT NULL DEFAULT '[]',
		true_since INTEGER NOT NULL DEFAULT 0,
		timed_history TEXT NOT NULL DEFAULT '[]',
		PRIMARY KEY (service, rule_name));
		INSERT INTO rule_window_state (service, rule_name, consecutive, history)
		VALUES ('web', 'unhealthy', 2, '[true,true]');`)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("create legacy database: %v; close: %v", err, closeErr)
	}
	want := RuleWindowRecord{Consecutive: 2, History: []bool{true, true}, TimedHistory: []rules.WindowSample{}}
	for range 2 {
		s, err := OpenContextWith(t.Context(), path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		got, err := s.RuleWindowStates("web")
		if err != nil || !reflect.DeepEqual(got["unhealthy"], want) {
			_ = s.Close()
			t.Fatalf("migrated progress = %+v, error = %v; want %+v", got, err, want)
		}
		want.Firing = true
		want.ClearSince = time.Unix(100, 0).UTC()
		want.ClearConsecutive = 1
		err = s.SetRuleWindowStates("web", map[string]RuleWindowRecord{"unhealthy": want})
		closeErr = s.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("persist migrated progress: %v; close: %v", err, closeErr)
		}
	}
}

func TestOperationSettlingMigrationPreservesTransitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, `CREATE TABLE operation_settling (
		service TEXT PRIMARY KEY, action TEXT NOT NULL, phase TEXT NOT NULL,
		source TEXT NOT NULL, updated_at TEXT NOT NULL);
		INSERT INTO operation_settling VALUES
		('web', '', 'running', '', '2026-09-25T10:00:00Z'),
		('db', '', 'settling', '', '2026-09-25T10:00:00Z');`)
	closeErr := db.Close()
	if err != nil || closeErr != nil {
		t.Fatalf("create legacy database: %v; close: %v", err, closeErr)
	}
	at := time.Date(2026, 9, 25, 10, 0, 0, 0, time.UTC)
	want := map[string]OperationSettlingRecord{
		"web": {Phase: OperationSettlingRunning, UpdatedAt: at},
		"db":  {Phase: OperationSettlingSettling, UpdatedAt: at},
	}
	for range 2 {
		s, err := OpenContextWith(ctx, path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		s.now = func() time.Time { return at }
		got, err := s.OperationSettlingStates()
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("migrated transitions = %+v, error = %v; want %+v", got, err, want)
		}
		var columns int
		if err := s.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM pragma_table_info('operation_settling')`,
		).Scan(&columns); err != nil || columns != 3 {
			t.Fatalf("settling column count = %d, error = %v; want 3", columns, err)
		}
		if err := s.SetOperationSettling("web", OperationSettlingSettling); err != nil {
			t.Fatal(err)
		}
		if err := s.ClearOperationSettling("db"); err != nil {
			t.Fatal(err)
		}
		want["web"] = OperationSettlingRecord{Phase: OperationSettlingSettling, UpdatedAt: at}
		delete(want, "db")
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

// Severity used to live in the event kind. Opening an old database moves it
// into its own column, so history and open incidents keep their grade.
func TestSeverityMigrationBackfillsEventsAndIncidents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE event_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT, at INTEGER NOT NULL,
			service TEXT NOT NULL DEFAULT '', watch TEXT NOT NULL DEFAULT '',
			kind TEXT NOT NULL DEFAULT '', rule TEXT NOT NULL DEFAULT '',
			action TEXT NOT NULL DEFAULT '', status TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '', app TEXT NOT NULL DEFAULT '',
			output TEXT NOT NULL DEFAULT '');`,
		`INSERT INTO event_log (at, watch, kind) VALUES (1, 'hdparm', 'warning'), (2, 'disk', 'firing'), (3, 'disk', 'recovered');`,
		`INSERT INTO event_log (at, service, kind, rule) VALUES (4, 'web', 'alert', 'mem');`,
		`INSERT INTO event_log (at, watch, kind, message) VALUES (5, 'hdparm', 'warning', 'check unavailable: no timing');`,
		`INSERT INTO event_log (at, watch, kind, action) VALUES (6, 'hdparm', 'warning', 'probe');`,
		`INSERT INTO event_log (at, service, kind, rule) VALUES (7, 'web', 'alert', 'service-restart');`,
		`CREATE TABLE event_notify_state (
			incident_key TEXT NOT NULL, notifier TEXT NOT NULL, phase TEXT NOT NULL,
			active INTEGER NOT NULL, last_sent_at INTEGER NOT NULL,
			subject TEXT NOT NULL, body TEXT NOT NULL,
			PRIMARY KEY (incident_key, notifier));`,
		`INSERT INTO event_notify_state VALUES ('w:hdparm', 'ops', 'warning', 1, 1, 's', 'b'), ('w:disk', 'ops', 'firing', 1, 2, 's', 'b'), ('w:net', 'ops', 'recovered', 0, 3, 's', 'b');`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := OpenContextWith(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("open over an old schema: %v", err)
	}
	defer func() { _ = s.Close() }()

	events, err := s.RecentEventsBefore(0, 0)
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64][2]string{}
	for _, e := range events {
		got[e.At.UnixNano()] = [2]string{e.Kind, e.Severity}
	}
	want := map[int64][2]string{
		1: {"firing", "warning"}, 2: {"firing", "error"}, 3: {"recovered", ""}, 4: {"alert", "error"},
		5: {"error", "warning"}, 6: {"error", "warning"}, 7: {"alert", "warning"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("migrated events = %v, want %v", got, want)
	}
	for key, want := range map[string][2]string{"w:hdparm": {"firing", "warning"}, "w:disk": {"firing", "error"}, "w:net": {"recovered", ""}} {
		rec, found, err := s.EventNotifyState(key, "ops")
		if err != nil || !found || rec.Phase != want[0] || rec.Severity != want[1] {
			t.Fatalf("incident %s = %+v (found %v, err %v), want phase %q severity %q", key, rec, found, err, want[0], want[1])
		}
	}
}

func TestSeverityStateRoundTrips(t *testing.T) {
	s := openTemp(t)
	rungs := []rules.EntryWindowSnapshot{
		{Consecutive: 3},
		{History: []bool{true, false}},
		{TrueSince: time.Unix(1_700_000_000, 0).UTC()},
		{TimedHistory: []rules.WindowSample{{At: time.Unix(1_700_000_060, 0).UTC()}}},
	}
	if err := s.SetRuleWindowStates("web", map[string]RuleWindowRecord{
		"mem": {Firing: true, Severity: "critical", NotifiedSeverity: "error", Rungs: rungs},
	}); err != nil {
		t.Fatal(err)
	}
	windows, err := s.RuleWindowStates("web")
	if err != nil {
		t.Fatal(err)
	}
	if rec := windows["mem"]; rec.Severity != "critical" || rec.NotifiedSeverity != "error" || !reflect.DeepEqual(rec.Rungs, rungs) {
		t.Fatalf("rule window = %+v", rec)
	}
	if err := s.SetWatchRuntimeState("disk", "result", WatchRuntimeRecord{Firing: true, Severity: "error", NotifiedSeverity: "warning", Window: WatchWindowRecord{Rungs: rungs}}); err != nil {
		t.Fatal(err)
	}
	watch, found, err := s.WatchRuntimeState("disk", "result")
	if err != nil || !found || watch.Severity != "error" || watch.NotifiedSeverity != "warning" || !reflect.DeepEqual(watch.Window.Rungs, rungs) {
		t.Fatalf("watch runtime = %+v (found %v, err %v)", watch, found, err)
	}
	// A RAID watch's delivered failure level is state on its own: it keeps the
	// row alive between episodes.
	if err := s.SetWatchRuntimeState("raid", "result", WatchRuntimeRecord{TransitionSeverity: "critical"}); err != nil {
		t.Fatal(err)
	}
	if raid, found, err := s.WatchRuntimeState("raid", "result"); err != nil || !found || raid.TransitionSeverity != "critical" {
		t.Fatalf("raid runtime = %+v (found %v, err %v)", raid, found, err)
	}
	// A watch whose rungs carry no progress has nothing to remember.
	if err := s.SetWatchRuntimeState("disk", "result", WatchRuntimeRecord{Window: WatchWindowRecord{Rungs: make([]rules.EntryWindowSnapshot, 4)}}); err != nil {
		t.Fatal(err)
	}
	if _, found, _ := s.WatchRuntimeState("disk", "result"); found {
		t.Fatal("idle rungs kept an otherwise empty watch row")
	}
	if _, err := s.RecordEvent(EventRecord{Watch: "disk", Kind: "firing", Severity: "critical"}); err != nil {
		t.Fatal(err)
	}
	if events, _ := s.RecentEventsBefore(0, 1); len(events) != 1 || events[0].Severity != "critical" {
		t.Fatalf("event severity = %+v", events)
	}
	if err := s.SetEventNotifyState(EventNotifyRecord{IncidentKey: "k", Notifier: "ops", Phase: "firing", Active: true, Severity: "warning"}); err != nil {
		t.Fatal(err)
	}
	due, err := s.DueEventNotifyStates("ops", time.Now())
	if err != nil || len(due) != 1 || due[0].Severity != "warning" {
		t.Fatalf("due reminders = %+v (err %v)", due, err)
	}
}
