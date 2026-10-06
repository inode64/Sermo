package app

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/notify"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
	"sermo/internal/severity"
	"sermo/internal/web"
)

// dbQueryFixture drives a dbQueryWatcher with a scripted sample.
type dbQueryFixture struct {
	w       *dbQueryWatcher
	queries []checks.DBQuery
	partial bool
	err     error
	events  []Event
	results []checks.Result
	now     time.Time
}

func newDBQueryFixture(t *testing.T) *dbQueryFixture {
	t.Helper()
	f := &dbQueryFixture{now: time.Unix(1_700_000_000, 0)}
	f.w = &dbQueryWatcher{
		name:     "mariadb:long-queries",
		severity: severity.Warning,
		cfg:      checks.DBQueryConfig{Engine: checks.SQLEngineMySQL, MinDuration: 5 * time.Minute, MaxRows: 50, Timeout: time.Second},
		now:      func() time.Time { return f.now },
		emit:     func(e Event) { f.events = append(f.events, e) },
		sample: func(context.Context, checks.DBQueryConfig) (checks.DBQuerySample, error) {
			return checks.DBQuerySample{Queries: append([]checks.DBQuery(nil), f.queries...), Complete: !f.partial}, f.err
		},
		publish: func(_, _ string, r checks.Result) { f.results = append(f.results, r) },
		procfs:  &fakeDBProcfs{},
	}
	return f
}

func (f *dbQueryFixture) cycle() []Event {
	before := len(f.events)
	f.w.runCycle(context.Background())
	return f.events[before:]
}

func mariaQuery(id, queryID, elapsed int64, user string) checks.DBQuery {
	return checks.DBQuery{
		Engine: checks.SQLEngineMariaDB, ID: id, QueryID: queryID, User: user, Database: "app",
		Command: "Query", State: "Sending data", ElapsedSeconds: elapsed, Query: "SELECT slow()",
		Identity: strconv.FormatInt(id, 10) + ":" + strconv.FormatInt(queryID, 10),
	}
}

func TestDBQueryWatchFiresOncePerStatementAndRecovers(t *testing.T) {
	f := newDBQueryFixture(t)
	f.queries = []checks.DBQuery{mariaQuery(7, 70, 60, "app")}
	if got := f.cycle(); len(got) != 0 {
		t.Fatalf("a short statement must not fire: %v", kinds(got))
	}
	f.queries = []checks.DBQuery{mariaQuery(8, 80, 239096, "tac_prod"), mariaQuery(7, 70, 400, "app")}
	got := f.cycle()
	if len(got) != 2 || got[0].Kind != eventKindFiring || got[1].Kind != eventKindFiring {
		t.Fatalf("two long statements must fire twice: %v", kinds(got))
	}
	if got[0].Check == got[1].Check || !strings.HasPrefix(got[0].Check, dbQueryEventCheckPrefix) {
		t.Fatalf("each statement is its own incident: %q %q", got[0].Check, got[1].Check)
	}
	if got[1].Severity != severity.Warning || !strings.Contains(got[0].Message, "SELECT slow()") || !strings.Contains(got[0].Message, "user tac_prod") {
		t.Fatalf("fire event = %+v", got[0])
	}
	if got := f.cycle(); len(got) != 0 {
		t.Fatalf("a statement still running must not fire again: %v", kinds(got))
	}
	// Statement 7 finished; connection 8 now runs another statement.
	f.queries = []checks.DBQuery{mariaQuery(8, 81, 10, "tac_prod")}
	got = f.cycle()
	if len(got) != 2 || got[0].Kind != eventKindRecovered || got[1].Kind != eventKindRecovered {
		t.Fatalf("both statements must recover: %v", kinds(got))
	}
	last := f.results[len(f.results)-1]
	if !last.OK || last.Data[checks.DataKeyLongCount] != 0 {
		t.Fatalf("snapshot after recovery = %+v", last)
	}
}

func TestDBQueryWatchRespectsFiltersAndObserveOnly(t *testing.T) {
	f := newDBQueryFixture(t)
	f.w.cfg.Selector = checks.DBQuerySelector{ExcludeUsers: []string{"backup"}}
	f.queries = []checks.DBQuery{mariaQuery(1, 10, 9000, "backup"), mariaQuery(2, 20, 9000, "app")}
	f.w.runCycle(withObserveOnly(context.Background(), true))
	if len(f.events) != 0 {
		t.Fatalf("an observe-only cycle must not fire: %v", kinds(f.events))
	}
	got := f.cycle()
	if len(got) != 1 || !strings.Contains(got[0].Message, "query 2 ") {
		t.Fatalf("only the unfiltered statement fires: %+v", got)
	}
	data := f.results[len(f.results)-1].Data
	if listed := data[checks.DataKeyDBQueries].([]checks.DBQuery); len(listed) != 2 || !listed[1].Alerted && !listed[0].Alerted {
		t.Fatalf("the snapshot lists every statement and marks the alerted one: %+v", listed)
	}
}

func TestDBQueryWatchRestoresAlertedStatements(t *testing.T) {
	f := newDBQueryFixture(t)
	running := mariaQuery(8, 80, 239096, "tac_prod")
	running.Alerted = true
	ended := mariaQuery(9, 90, 600, "app")
	ended.Alerted = true
	f.w.restore = func() []checks.DBQuery { return []checks.DBQuery{running, ended} }
	f.queries = []checks.DBQuery{mariaQuery(8, 80, 239126, "tac_prod")}
	got := f.cycle()
	if len(got) != 1 || got[0].Kind != eventKindRecovered || !strings.Contains(got[0].Message, "query 9 ") {
		t.Fatalf("a restart must not re-announce a running statement, and must recover one that ended: %+v", got)
	}
}

func TestDBQueryWatchReportsUnavailableOnce(t *testing.T) {
	f := newDBQueryFixture(t)
	f.err = errors.New("Access denied for user 'root'@'localhost'")
	first, second := f.cycle(), f.cycle()
	if len(first) != 1 || first[0].Kind != eventKindError || first[0].Check != watchAvailabilityCheck || len(second) != 0 {
		t.Fatalf("unavailable must be reported once: %v %v", kinds(first), kinds(second))
	}
	if !f.results[len(f.results)-1].Unavailable {
		t.Fatal("the snapshot must be unavailable")
	}
	f.err = nil
	if got := f.cycle(); len(got) != 1 || got[0].Kind != eventKindRecovered {
		t.Fatalf("availability must recover once: %v", kinds(got))
	}
}

func TestDBQueryWatchSkipsAStoppedService(t *testing.T) {
	f := newDBQueryFixture(t)
	f.w.status = func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusInactive, nil }
	f.w.sample = func(context.Context, checks.DBQueryConfig) (checks.DBQuerySample, error) {
		t.Fatal("a stopped service must not be sampled")
		return checks.DBQuerySample{}, nil
	}
	if got := f.cycle(); len(got) != 0 || !f.results[0].Skipped {
		t.Fatalf("events = %v result = %+v", kinds(got), f.results)
	}
}

func TestDBQueryWatchAutoKill(t *testing.T) {
	f := newDBQueryFixture(t)
	var targets []operation.DBQueryTarget
	f.w.kill = &checks.DBQueryKillSpec{After: 30 * time.Minute, Mode: checks.DBQueryKillModeQuery, Selector: checks.DBQuerySelector{Users: []string{"report"}}}
	f.w.policy = rules.Policy{Cooldown: 10 * time.Minute}
	f.w.killer = func(_ context.Context, target operation.DBQueryTarget) operation.Result {
		targets = append(targets, target)
		return operation.Result{Status: operation.ResultOK, Action: string(rules.ActionKillQuery), Message: "kill query: cancelled"}
	}
	f.queries = []checks.DBQuery{
		mariaQuery(3, 30, 7200, "report"),
		mariaQuery(1, 10, 3600, "tac_prod"), // long, but not selected
		mariaQuery(2, 20, 3600, "report"),
	}
	got := f.cycle()
	if len(targets) != 1 || targets[0].ID != 3 || targets[0].Watch != "long-queries" || targets[0].Require == nil {
		t.Fatalf("one kill per cycle, longest selected first: %+v", targets)
	}
	if kinds(got)[len(got)-1] != eventKindKill {
		t.Fatalf("events = %v", kinds(got))
	}
	// Statement 2 is eligible too, but the cooldown holds it back (reported once).
	got = f.cycle()
	got = append(got, f.cycle()...)
	if len(targets) != 1 || len(got) != 1 || got[0].Kind != eventKindSuppressed {
		t.Fatalf("cooldown: targets=%d events=%v", len(targets), kinds(got))
	}
	f.now = f.now.Add(11 * time.Minute)
	f.cycle()
	if len(targets) != 2 || targets[1].ID != 2 {
		t.Fatalf("after the cooldown the next statement is killed: %+v", targets)
	}
}

func TestDBQueryWatchAutoKillDryRun(t *testing.T) {
	f := newDBQueryFixture(t)
	f.w.dryRun = true
	f.w.kill = &checks.DBQueryKillSpec{After: time.Minute, Mode: checks.DBQueryKillModeQuery, Selector: checks.DBQuerySelector{Users: []string{"report"}}}
	f.w.policy = rules.Policy{Cooldown: time.Minute}
	f.w.killer = func(context.Context, operation.DBQueryTarget) operation.Result {
		t.Fatal("dry-run must not kill")
		return operation.Result{}
	}
	f.queries = []checks.DBQuery{mariaQuery(2, 20, 3600, "report")}
	got := f.cycle()
	if kinds(got)[len(got)-1] != eventKindDryRun || !strings.Contains(got[len(got)-1].Message, "would kill_query") {
		t.Fatalf("events = %+v", got)
	}
	if got := f.cycle(); len(got) != 0 {
		t.Fatalf("the would-kill is reported once per statement: %v", kinds(got))
	}
}

func TestDBQueryKillerResolvesTheServiceWatch(t *testing.T) {
	tree := map[string]any{"watches": map[string]any{
		"long-queries": map[string]any{"check": map[string]any{"type": "db_queries", "engine": "mariadb", "min_duration": "5m"}},
		"other":        map[string]any{"check": map[string]any{"type": "tcp", "port": 3306}},
	}}
	var gotCfg checks.DBQueryConfig
	killer := dbQueryKiller(tree, func(_ context.Context, cfg checks.DBQueryConfig, req checks.DBQueryKill) (checks.DBQuery, error) {
		gotCfg = cfg
		return mariaQuery(req.ID, 70, 500, "app"), nil
	})
	msg, err := killer(context.Background(), operation.DBQueryTarget{Watch: "long-queries", ID: 7, Identity: "7:70", Mode: "query"})
	if err != nil || gotCfg.MinDuration != 5*time.Minute || !strings.Contains(msg, "cancelled mariadb query 7") {
		t.Fatalf("msg=%q err=%v cfg=%+v", msg, err, gotCfg)
	}
	if _, err := killer(context.Background(), operation.DBQueryTarget{Watch: "other"}); err == nil {
		t.Fatal("a watch that is not db_queries must be refused")
	}
	if dbQueryKiller(map[string]any{}, nil) != nil {
		t.Fatal("a service without db_queries watches has no killer")
	}
}

func TestWebBackendListsDBQueriesFromSnapshots(t *testing.T) {
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	snapshots := NewWatchSnapshots()
	snapshots.now = func() time.Time { return now }
	long := mariaQuery(8, 80, 239096, "tac_prod")
	long.Alerted = true
	long.Long = true
	long.OSThreadID = 4102
	long.CPU, long.CPUThread, long.CPUReady = 12.5, 100, true
	short := mariaQuery(9, 90, 30, "app")
	killed := mariaQuery(10, 100, 900, "app")
	killed.Command = "Killed"
	snapshots.publishConfigured("mariadb:long-queries", checks.CheckTypeDBQueries, checks.Result{
		Check: "mariadb:long-queries", OK: false,
		Data: map[string]any{checks.DataKeyDBQueries: []checks.DBQuery{short, long, killed}},
	}, "")
	snapshots.publishConfigured("pg", checks.CheckTypeDBQueries, checks.Result{
		Check: "pg", Unavailable: true, Message: "postgres: connection refused",
	}, "")
	check := map[string]any{"type": "db_queries", "min_duration": "5m"}
	b := &WebBackend{
		now:        func() time.Time { return now },
		watchOrder: []string{"mariadb:long-queries", "pg", "stale"},
		watches: map[string]*webWatch{
			"mariadb:long-queries": {name: "mariadb:long-queries", checkType: checks.CheckTypeDBQueries, check: check, interval: time.Minute, serviceScoped: true},
			"pg":                   {name: "pg", checkType: checks.CheckTypeDBQueries, check: check, interval: time.Minute},
			"stale":                {name: "stale", checkType: checks.CheckTypeDBQueries, check: check, interval: time.Minute},
		},
		watchSnapshots: snapshots,
	}
	var inv web.SessionInventory
	b.appendDBQueries(&inv)
	if len(inv.Sources) != 3 || inv.Sources[0].State != web.SessionSourceAvailable || inv.Sources[0].Service != "mariadb" ||
		inv.Sources[0].Check != "long-queries" || inv.Sources[1].State != web.SessionSourceUnavailable || inv.Sources[2].State != web.SessionSourceCollecting {
		t.Fatalf("sources = %+v", inv.Sources)
	}
	if len(inv.Database) != 3 || inv.Database[0].ID != 8 || !inv.Database[0].Long || !inv.Database[0].CanKill {
		t.Fatalf("rows = %+v", inv.Database)
	}
	if row := inv.Database[0]; row.QueryID != 80 || row.OSThreadID != 4102 || row.At != now.Format(time.RFC3339) {
		t.Fatalf("statement metadata = %+v", row)
	}
	if row := inv.Database[0]; !row.CPUReady || row.CPU != 12.5 || row.CPUThread != 100 {
		t.Fatalf("statement CPU readings = %+v", row)
	}
	for _, row := range inv.Database {
		if row.ID == 10 && row.CanKill {
			t.Fatal("a statement already being killed cannot be killed again")
		}
		if row.ID == 9 && row.Long {
			t.Fatal("a short statement is not long")
		}
		if row.ID == 9 && row.OSThreadID != 0 {
			t.Fatal("a missing OS thread ID must not use the connection ID")
		}
	}
	// Resource warmup retains the live list; a database failure's held
	// incidents must still never be presented as current statements.
	snapshots.publishConfigured("mariadb:long-queries", checks.CheckTypeDBQueries, checks.Result{
		Check: "mariadb:long-queries", Unavailable: true, Message: "resource readings unavailable",
		Data: map[string]any{checks.DataKeyCount: 1, checks.DataKeyUnknownCount: 1, checks.DataKeyDBQueries: []checks.DBQuery{short}},
	}, "")
	var pending web.SessionInventory
	b.appendDBQueries(&pending)
	if len(pending.Database) != 1 || pending.Sources[0].State != web.SessionSourcePartial {
		t.Fatalf("resource warmup must keep current statements visible: %+v", pending)
	}
	for _, data := range []map[string]any{
		{checks.DataKeyCount: 1, checks.DataKeyUnknownCount: 1, checks.DataKeyMatchedCount: 1, checks.DataKeyComplete: true, checks.DataKeyDBQueries: []checks.DBQuery{long}},
		{checks.DataKeyCount: 1, checks.DataKeyComplete: false, checks.DataKeyDBQueries: []checks.DBQuery{long}},
	} {
		snapshots.publishConfigured("mariadb:long-queries", checks.CheckTypeDBQueries, checks.Result{
			Check: "mariadb:long-queries", Message: "partial sample", Data: data,
		}, "")
		var partial web.SessionInventory
		b.appendDBQueries(&partial)
		if len(partial.Database) != 1 || partial.Sources[0].State != web.SessionSourcePartial || partial.Sources[0].Message != "partial sample" {
			t.Fatalf("known failure hid incomplete data: %+v", partial)
		}
	}
	// An expired sample must not keep a statement or its metadata in the UI.
	now = now.Add(4 * time.Minute)
	var expired web.SessionInventory
	b.appendDBQueries(&expired)
	if len(expired.Database) != 0 || expired.Sources[0].State != web.SessionSourceCollecting {
		t.Fatalf("expired inventory = %+v", expired)
	}
}

// fakeDBProcfs serves per-thread counters keyed by tid, read the way the
// watcher reads them: /proc/<tid>/task/<tid>.
type fakeDBProcfs struct {
	exe         string
	exeCalls    int
	ticks       map[int]uint64
	read, write map[int]uint64
	rss         map[int]uint64
}

func (*fakeDBProcfs) NumCPU() int { return 8 }

func (f *fakeDBProcfs) ThreadCPU(pid, tid int) (uint64, bool) {
	v, ok := f.ticks[tid]
	return v, ok && pid == tid
}
func (f *fakeDBProcfs) ThreadIO(pid, tid int) (uint64, uint64, bool) {
	r, ok := f.read[tid]
	return r, f.write[tid], ok && pid == tid
}
func (f *fakeDBProcfs) ProcessRSS(pid int) (uint64, bool) { v, ok := f.rss[pid]; return v, ok }

// ThreadExe answers like a local server's thread unless exe overrides it.
func (f *fakeDBProcfs) ThreadExe(int) (string, bool) {
	f.exeCalls++
	if f.exe != "" {
		return f.exe, true
	}
	return "/usr/sbin/mariadbd", true
}

func TestDBQueryWatchMeasuresTheStatementThread(t *testing.T) {
	f := newDBQueryFixture(t)
	f.w.cfg.Conn.Socket = "/run/mysqld/mysqld.sock"
	const tid = 1168916
	proc := &fakeDBProcfs{
		ticks: map[int]uint64{tid: 1000},
		read:  map[int]uint64{tid: 1 << 30},
		write: map[int]uint64{tid: 0},
	}
	f.w.procfs = proc
	q := mariaQuery(8, 80, 600, "tac_prod")
	q.OSThreadID, q.MemoryBytes, q.MemoryReady = tid, 226448, true
	f.queries = []checks.DBQuery{q}
	f.cycle()
	first := f.results[len(f.results)-1].Data[checks.DataKeyDBQueries].([]checks.DBQuery)[0]
	if first.CPUReady || first.IOReady || first.MemoryBytes != 226448 || !first.MemoryReady {
		t.Fatalf("first sample: rates need a baseline, memory comes from the server: %+v", first)
	}
	f.now = f.now.Add(10 * time.Second)
	proc.ticks[tid] += 1000            // 10 CPU-seconds in 10s at 100 Hz
	proc.read[tid] += 10 * (1 << 20)   // 1 MiB/s
	proc.write[tid] += 10 * (64 << 10) // 64 KiB/s
	f.cycle()
	second := f.results[len(f.results)-1].Data[checks.DataKeyDBQueries].([]checks.DBQuery)[0]
	if !second.CPUReady || second.CPU != 12.5 || second.CPUThread != 100 || !second.IOReady || second.IORead != 1<<20 || second.IOWrite != 64<<10 {
		t.Fatalf("second sample must carry the thread's CPU and IO rates: %+v", second)
	}

	// A remote server's thread ids name nothing on this host.
	f.w.cfg.Conn.Socket, f.w.cfg.Conn.Host = "", "10.0.0.5"
	f.now = f.now.Add(10 * time.Second)
	f.cycle()
	if remote := f.results[len(f.results)-1].Data[checks.DataKeyDBQueries].([]checks.DBQuery)[0]; remote.CPUReady || remote.IOReady {
		t.Fatalf("a remote statement must stay unmeasured: %+v", remote)
	}
}

func TestDBQueryWatchCPUReadiness(t *testing.T) {
	for _, tt := range []struct {
		name        string
		ticks       uint64
		elapsed     time.Duration
		missing     bool
		replacement bool
		wantReady   bool
	}{
		{name: "idle", ticks: 1000, elapsed: time.Second, wantReady: true},
		{name: "counter reset", ticks: 999, elapsed: time.Second},
		{name: "no elapsed time", ticks: 1000},
		{name: "unreadable thread", missing: true, elapsed: time.Second},
		{name: "replacement statement", ticks: 1100, elapsed: time.Second, replacement: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDBQueryFixture(t)
			f.w.cfg.Conn.Host = "127.0.0.1"
			proc := &fakeDBProcfs{ticks: map[int]uint64{4102: 1000}}
			f.w.procfs = proc
			q := mariaQuery(8, 80, 600, "app")
			q.OSThreadID = 4102
			f.queries = []checks.DBQuery{q}
			f.cycle()
			f.now = f.now.Add(tt.elapsed)
			proc.ticks[4102] = tt.ticks
			if tt.missing {
				delete(proc.ticks, 4102)
			}
			if tt.replacement {
				f.queries[0].Identity = "8:81"
			}
			f.cycle()
			got := checks.DBQueriesFromData(f.results[len(f.results)-1].Data)[0]
			if got.CPUReady != tt.wantReady || got.CPU != 0 || got.CPUThread != 0 {
				t.Fatalf("CPU readings = (%v, %v, %v), want (0, 0, %v)", got.CPU, got.CPUThread, got.CPUReady, tt.wantReady)
			}
		})
	}
}

func TestDBQueryWatchMeasuresPostgresBackendMemory(t *testing.T) {
	f := newDBQueryFixture(t)
	f.w.cfg.Conn.Host = "127.0.0.1"
	f.w.procfs = &fakeDBProcfs{exe: "/usr/lib/postgresql/17/bin/postgres", rss: map[int]uint64{4711: 32 << 20}}
	f.queries = []checks.DBQuery{{Engine: checks.SQLEnginePostgres, ID: 4711, OSThreadID: 4711, User: "app", ElapsedSeconds: 10, Identity: "4711:1:2"}}
	f.cycle()
	got := f.results[len(f.results)-1].Data[checks.DataKeyDBQueries].([]checks.DBQuery)[0]
	if !got.MemoryReady || got.MemoryBytes != 32<<20 {
		t.Fatalf("postgres backend memory = %+v", got)
	}
}

// A sampleless snapshot (server unreachable, service stopped) still carries
// the alerted statements, so a restart meanwhile keeps their incidents.
func TestDBQueryWatchKeepsAlertedStatementsInSamplelessSnapshots(t *testing.T) {
	f := newDBQueryFixture(t)
	f.queries = []checks.DBQuery{mariaQuery(8, 80, 600, "tac_prod")}
	f.cycle()
	f.err = errors.New("connection refused")
	f.cycle()
	held := checks.DBQueryIncidentsFromData(f.results[len(f.results)-1].Data)
	if len(held) != 1 || !held[0].Alerted || held[0].ID != 8 {
		t.Fatalf("an unavailable snapshot must keep the alerted statement: %+v", held)
	}
}

// The persisted list is cut to max_rows, but never drops an alerted statement.
func TestDBQueryWatchPersistsAlertedStatementsBeyondMaxRows(t *testing.T) {
	f := newDBQueryFixture(t)
	f.w.cfg.MaxRows = 1
	f.w.cfg.Selector = checks.DBQuerySelector{ExcludeUsers: []string{"backup"}}
	f.queries = []checks.DBQuery{mariaQuery(1, 10, 9000, "backup"), mariaQuery(2, 20, 600, "app")}
	f.cycle()
	listed := checks.DBQueriesFromData(f.results[len(f.results)-1].Data)
	if len(listed) != 2 || listed[1].ID != 2 || !listed[1].Alerted {
		t.Fatalf("listed = %+v", listed)
	}
}

// An alert a live notifier heard is recovered to it, even across a restart.
func TestDBQueryWatchRecoversAnnouncedStatementsAfterRestart(t *testing.T) {
	n := &fakeNotifier{name: "slack"}
	f := newDBQueryFixture(t)
	f.w.notifiers = []notify.Notifier{n}
	q := mariaQuery(8, 80, 600, "tac_prod")
	q.Alerted, q.Announced = true, true
	f.w.restore = func() []checks.DBQuery { return []checks.DBQuery{q} }
	f.queries = nil
	f.cycle()
	if len(n.msgs) != 1 || !strings.HasPrefix(n.msgs[0].Body, recoveredMessagePrefix) {
		t.Fatalf("the restored announced statement must recover to its notifier: %+v", n.msgs)
	}
}

// A hook-only watch runs its hook on the recovery too.
func TestDBQueryWatchRunsTheHookOnRecovery(t *testing.T) {
	f := newDBQueryFixture(t)
	var envs []map[string]string
	f.w.hook = HookSpec{Command: []string{"/usr/local/bin/slowq"}}
	f.w.runner = HookRunnerFunc(func(_ context.Context, _ []string, env map[string]string, _ time.Duration) error {
		envs = append(envs, env)
		return nil
	})
	f.queries = []checks.DBQuery{mariaQuery(8, 80, 600, "tac_prod")}
	f.cycle()
	f.queries = nil
	f.cycle()
	if len(envs) != 2 || envs[1][sermoEnvChange] != dbQueryChangeEnded {
		t.Fatalf("hook runs = %+v", envs)
	}
}

func TestDBQueryWatchAnnouncementUsesTheDispatchDecision(t *testing.T) {
	for _, tt := range []struct {
		name          string
		dryRun, panic bool
	}{
		{name: "live"},
		{name: "panic", panic: true},
		{name: "dry run", dryRun: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newDBQueryFixture(t)
			n := &fakeNotifier{name: "ops"}
			f.w.notifiers, f.w.dryRun = []notify.Notifier{n}, tt.dryRun
			calls := 0
			f.w.inPanic = func() bool {
				calls++
				if calls == 1 {
					return tt.panic
				}
				return !tt.panic // A second read would contradict the first.
			}
			f.queries = []checks.DBQuery{mariaQuery(8, 80, 600, "app")}
			f.cycle()
			q := checks.DBQueriesFromData(f.results[0].Data)[0]
			want := !tt.dryRun && !tt.panic
			if q.Announced != want || (len(n.msgs) == 1) != want || calls > 1 {
				t.Fatalf("announced=%v messages=%d panic reads=%d, want live=%v", q.Announced, len(n.msgs), calls, want)
			}
			f.w.inPanic, f.w.dryRun, f.queries = nil, false, nil
			f.cycle()
			if (len(n.msgs) == 2) != want {
				t.Fatalf("recovery messages=%d, want recovery=%v", len(n.msgs), want)
			}
		})
	}
}

// The automatic kill never stops a statement the check's own filters exclude.
func TestDBQueryWatchAutoKillHonoursTheCheckFilters(t *testing.T) {
	f := newDBQueryFixture(t)
	f.w.cfg.Selector = checks.DBQuerySelector{ExcludeUsers: []string{"replicator"}}
	f.w.kill = &checks.DBQueryKillSpec{After: time.Minute, Mode: checks.DBQueryKillModeQuery, Selector: checks.DBQuerySelector{Databases: []string{"app"}}}
	f.w.policy = rules.Policy{Cooldown: time.Minute}
	var targets []operation.DBQueryTarget
	f.w.killer = func(_ context.Context, target operation.DBQueryTarget) operation.Result {
		targets = append(targets, target)
		return operation.Result{Status: operation.ResultOK}
	}
	f.queries = []checks.DBQuery{mariaQuery(1, 10, 3600, "replicator")}
	f.cycle()
	if len(targets) != 0 {
		t.Fatalf("an excluded user's statement must not be killed: %+v", targets)
	}
	f.queries = []checks.DBQuery{mariaQuery(2, 20, 3600, "app")}
	f.cycle()
	if len(targets) != 1 || targets[0].Require.Filter.ExcludeUsers[0] != "replicator" {
		t.Fatalf("the kill must carry the check filter for re-verification: %+v", targets)
	}
}

// Panic mode holds the automatic kill back; it acts once panic clears.
func TestDBQueryWatchAutoKillResumesAfterPanic(t *testing.T) {
	f := newDBQueryFixture(t)
	panic := true
	f.w.inPanic = func() bool { return panic }
	f.w.kill = &checks.DBQueryKillSpec{After: time.Minute, Mode: checks.DBQueryKillModeQuery, Selector: checks.DBQuerySelector{Users: []string{"report"}}}
	f.w.policy = rules.Policy{Cooldown: time.Minute}
	kills := 0
	f.w.killer = func(context.Context, operation.DBQueryTarget) operation.Result {
		kills++
		return operation.Result{Status: operation.ResultOK}
	}
	f.queries = []checks.DBQuery{mariaQuery(2, 20, 3600, "report")}
	f.cycle()
	f.cycle()
	if kills != 0 {
		t.Fatal("panic mode must hold the kill back")
	}
	panic = false
	f.cycle()
	if kills != 1 {
		t.Fatalf("the kill must run once panic clears: %d", kills)
	}
}

// A kill that stopped nothing (the statement ended first) spends no budget.
func TestDBQueryWatchFailedKillSpendsNoPolicy(t *testing.T) {
	f := newDBQueryFixture(t)
	f.w.kill = &checks.DBQueryKillSpec{After: time.Minute, Mode: checks.DBQueryKillModeQuery, Selector: checks.DBQuerySelector{Users: []string{"report"}}}
	f.w.policy = rules.Policy{Cooldown: time.Hour}
	kills := 0
	f.w.killer = func(context.Context, operation.DBQueryTarget) operation.Result {
		kills++
		if kills == 1 {
			return operation.Result{Status: operation.ResultFailed, Message: checks.ErrDBQueryChanged.Error()}
		}
		return operation.Result{Status: operation.ResultOK}
	}
	f.queries = []checks.DBQuery{mariaQuery(2, 20, 3600, "report"), mariaQuery(3, 30, 3500, "report")}
	f.cycle()
	f.cycle()
	if kills != 2 {
		t.Fatalf("a failed kill must not hold the next one back for the cooldown: %d kills", kills)
	}
}

// A thread that does not belong to the database server (a container's ids on
// the host) is never measured.
func TestDBQueryWatchMeasuresOnlyServerThreads(t *testing.T) {
	f := newDBQueryFixture(t)
	f.w.cfg.Conn.Socket = "/run/mysqld/mysqld.sock"
	f.w.procfs = &fakeDBProcfs{exe: "/usr/bin/bash", ticks: map[int]uint64{42: 1}, read: map[int]uint64{42: 1}, write: map[int]uint64{42: 1}}
	q := mariaQuery(8, 80, 600, "app")
	q.OSThreadID = 42
	f.queries = []checks.DBQuery{q}
	f.cycle()
	f.now = f.now.Add(time.Minute)
	f.cycle()
	if got := checks.DBQueriesFromData(f.results[len(f.results)-1].Data)[0]; got.CPUReady || got.IOReady {
		t.Fatalf("a foreign thread was measured: %+v", got)
	}
}
