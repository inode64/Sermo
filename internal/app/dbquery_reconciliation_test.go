package app

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/operation"
	"sermo/internal/rules"
	"sermo/internal/web"
)

func TestDBQueryPartialReplacementRecoversAcrossRestart(t *testing.T) {
	for _, engine := range []string{checks.SQLEngineMySQL, checks.SQLEngineMariaDB, checks.SQLEnginePostgres} {
		t.Run(engine, func(t *testing.T) {
			f := newDBQueryFixture(t)
			old := mariaQuery(7, 70, 600, "app")
			old.Engine, old.Fingerprint, old.StartedUnixMS = engine, "first", 10000
			unseen := mariaQuery(8, 80, 900, "app")
			hooks := 0
			f.w.hook = HookSpec{Command: []string{"/hook"}}
			f.w.runner = HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { hooks++; return nil })
			f.queries = []checks.DBQuery{unseen, old}
			f.cycle()
			next := old
			next.Identity, next.QueryID, next.Fingerprint = "7:71", 71, "second"
			next.StartedUnixMS, next.ElapsedSeconds = 20000, 10
			f.partial, f.queries = true, []checks.DBQuery{next}
			before := len(f.events)
			f.w.runCycle(withObserveOnly(t.Context(), true))
			if len(f.events) != before || hooks != 2 {
				t.Fatalf("observation recovered a replaced statement: events=%v hooks=%d", kinds(f.events[before:]), hooks)
			}
			data := persistedDBQueryData(t, f.results[len(f.results)-1].Data)
			f.w.state = nil
			f.w.restore = func() []checks.DBQuery { return checks.DBQueryIncidentsFromData(data) }
			events := f.cycle()
			if len(events) != 2 || events[0].Kind != eventKindRecovered || events[0].Check != dbQueryEventCheck(old.ItemKey()) || events[1].Kind != eventKindHook || hooks != 3 {
				t.Fatalf("replacement did not recover exactly once: events=%+v hooks=%d", events, hooks)
			}
			held := checks.DBQueryIncidentsFromData(f.results[len(f.results)-1].Data)
			if len(held) != 1 || held[0].Identity != unseen.Identity {
				t.Fatalf("partial sample must retain only the unobserved connection: %+v", held)
			}
			if events := f.cycle(); len(events) != 0 || hooks != 3 {
				t.Fatalf("repeated recovery: events=%v hooks=%d", kinds(events), hooks)
			}
		})
	}
}

func TestDBQueryPartialConnectionChurnDoesNotAccumulateIncidents(t *testing.T) {
	f := newDBQueryFixture(t)
	f.partial = true
	for cycle := range 4 {
		f.queries = nil
		for id := int64(1); id <= 500; id++ {
			f.queries = append(f.queries, mariaQuery(id, int64(cycle)*1000+id, 600, "app"))
		}
		f.cycle()
		held := checks.DBQueryIncidentsFromData(f.results[len(f.results)-1].Data)
		if len(f.w.state) != 500 || len(held) != 500 {
			t.Fatalf("cycle %d: 500 connections retained %d states and %d incidents", cycle, len(f.w.state), len(held))
		}
	}
}

func TestDBQueryDisplayIdentityPreservesMySQLStatementAndLatestKillIdentity(t *testing.T) {
	f := newDBQueryFixture(t)
	q := checks.DBQuery{Engine: checks.SQLEngineMySQL, ID: 7, Fingerprint: "abcd:20", ElapsedSeconds: 600}
	snapshots := NewWatchSnapshots()
	b := &WebBackend{
		watchSnapshots: snapshots, watchOrder: []string{f.w.name},
		watches: map[string]*webWatch{f.w.name: {name: f.w.name, checkType: checks.CheckTypeDBQueries, serviceScoped: true}},
	}
	for _, tt := range []struct {
		start   int64
		display string
		restart bool
	}{
		{start: 10000, display: "7:abcd:20:10000"},
		{start: 10500, display: "7:abcd:20:10000"},
		{start: 10100, display: "7:abcd:20:10000", restart: true},
		{start: 20000, display: "7:abcd:20:20000"},
	} {
		if tt.restart {
			data := persistedDBQueryData(t, f.results[len(f.results)-1].Data)
			f.w.state = nil
			f.w.restore = func() []checks.DBQuery { return checks.DBQueryIncidentsFromData(data) }
		}
		q.StartedUnixMS, q.Identity = tt.start, "7:abcd:20:"+strconv.FormatInt(tt.start, 10)
		f.queries = []checks.DBQuery{q}
		f.cycle()
		snapshots.publishConfigured(f.w.name, checks.CheckTypeDBQueries, f.results[len(f.results)-1], "")
		var inventory web.SessionInventory
		b.appendDBQueries(&inventory)
		if len(inventory.Database) != 1 {
			t.Fatalf("statement list = %+v", inventory.Database)
		}
		row := inventory.Database[0]
		if row.Identity != q.Identity || row.DisplayIdentity != tt.display || !row.CanKill {
			t.Fatalf("start %d: presentation changed or kill identity became stale: %+v", tt.start, row)
		}
	}
}

func TestDBQueryObservationDefersRecoveryAcrossRestart(t *testing.T) {
	for _, replacement := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent", true: "same-key replacement"}[replacement], func(t *testing.T) {
			f := newDBQueryFixture(t)
			old := checks.DBQuery{Engine: checks.SQLEngineMySQL, ID: 7, Fingerprint: "same-text", Identity: "old", StartedUnixMS: 10000, ElapsedSeconds: 600, Alerted: true, Announced: true}
			f.w.restore = func() []checks.DBQuery { return []checks.DBQuery{old} }
			if replacement {
				current := old
				current.StartedUnixMS, current.Identity, current.ElapsedSeconds = 20000, "new", 10
				current.Alerted, current.Announced = false, false
				f.queries = []checks.DBQuery{current}
			}
			hooks := 0
			f.w.hook = HookSpec{Command: []string{"/hook"}}
			f.w.runner = HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { hooks++; return nil })
			f.w.runCycle(withObserveOnly(t.Context(), true))
			if len(f.events) != 0 || hooks != 0 {
				t.Fatalf("observation caused effects: events=%v hooks=%d", kinds(f.events), hooks)
			}
			data := persistedDBQueryData(t, f.results[len(f.results)-1].Data)
			if held := checks.DBQueryIncidentsFromData(data); len(held) != 1 || held[0].Identity != old.Identity {
				t.Fatalf("pending recovery was not persisted: %+v", held)
			}
			for _, q := range checks.DBQueriesFromData(data) {
				if q.Identity == old.Identity || q.Alerted {
					t.Fatalf("old incident appeared in the current list: %+v", q)
				}
			}
			// Restart before the first live cycle: pending recovery must survive.
			snapshots := NewWatchSnapshots()
			snapshots.publishConfigured(f.w.name, checks.CheckTypeDBQueries, checks.Result{Check: f.w.name, Data: data}, "")
			f.w.state = nil
			f.w.restore = func() []checks.DBQuery { return restoreDBQueries(snapshots, f.w.name) }
			got := f.cycle()
			if hooks != 1 || len(got) != 2 || got[0].Kind != eventKindRecovered || got[1].Kind != eventKindHook {
				t.Fatalf("live recovery = %v hooks=%d", kinds(got), hooks)
			}
			if got := f.cycle(); len(got) != 0 || hooks != 1 {
				t.Fatalf("repeated recovery = %v hooks=%d", kinds(got), hooks)
			}
		})
	}
}

func persistedDBQueryData(t *testing.T, data map[string]any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestDBQueryPartialSampleRetainsIncidentsWithoutActingOnOldRows(t *testing.T) {
	f := newDBQueryFixture(t)
	old := mariaQuery(1, 10, 9000, "app")
	old.Alerted, old.Long = true, true
	f.w.restore = func() []checks.DBQuery { return []checks.DBQuery{old} }
	f.w.kill = &checks.DBQueryKillSpec{After: 30 * time.Minute, Mode: checks.DBQueryKillModeQuery, Selector: checks.DBQuerySelector{Users: []string{"app"}}}
	f.w.policy = rules.Policy{Cooldown: time.Minute}
	var killed []int64
	f.w.killer = func(_ context.Context, target operation.DBQueryTarget) operation.Result {
		killed = append(killed, target.ID)
		return operation.Result{Status: operation.ResultOK}
	}
	f.partial = true
	f.queries = []checks.DBQuery{mariaQuery(2, 20, 10, "app")}
	if got := f.cycle(); len(got) != 0 || len(killed) != 0 {
		t.Fatalf("partial absence caused effects: %v killed=%v", kinds(got), killed)
	}
	r := f.results[len(f.results)-1]
	if r.OK || !r.Unavailable || r.Data[checks.DataKeyComplete] != false {
		t.Fatalf("incomplete sample claimed health: %+v", r)
	}
	data := persistedDBQueryData(t, r.Data)
	if held := checks.DBQueryIncidentsFromData(data); len(held) != 1 || held[0].ID != 1 {
		t.Fatalf("lost open incident: %+v", held)
	}
	if listed := checks.DBQueriesFromData(data); len(listed) != 1 || listed[0].ID != 2 {
		t.Fatalf("unobserved statement listed as current: %+v", listed)
	}
	f.w.state = nil
	f.w.restore = func() []checks.DBQuery { return checks.DBQueryIncidentsFromData(data) }
	if got := f.cycle(); len(got) != 0 || len(killed) != 0 {
		t.Fatalf("restart acted on an old row: %v killed=%v", kinds(got), killed)
	}
	// Only a current statement can be considered for automatic cancellation.
	f.queries[0].ElapsedSeconds = 3600
	f.cycle()
	if len(killed) != 1 || killed[0] != 2 {
		t.Fatalf("automatic kill selected an unobserved incident: %v", killed)
	}
	f.partial = false
	got := f.cycle()
	if len(got) != 1 || got[0].Kind != eventKindRecovered || got[0].Check != dbQueryEventCheck(old.ItemKey()) {
		t.Fatalf("complete sample did not recover missing incident: %+v", got)
	}
}

func TestDBQueryResourceAgeGateDoesNotMarkDuration(t *testing.T) {
	f := newDBQueryFixture(t)
	cfg, err := checks.ParseDBQueryConfig(map[string]any{"engine": "mariadb", "min_duration": "30s", "cpu_thread": map[string]any{"op": ">", "value": 90}})
	if err != nil {
		t.Fatal(err)
	}
	f.w.cfg = cfg
	q := mariaQuery(7, 70, 60, "app")
	q.CPUReady, q.CPUThread = true, 10
	f.queries = []checks.DBQuery{q}
	for _, hot := range []bool{false, true} {
		if hot {
			f.queries[0].CPUThread = 100
		}
		f.cycle()
		r := f.results[len(f.results)-1]
		listed := checks.DBQueriesFromData(r.Data)[0]
		if listed.Long || listed.Matched != hot || r.Data[checks.DataKeyLongCount] != 0 || r.OK == hot {
			t.Fatalf("resource age gate claimed duration: %+v result=%+v", listed, r)
		}
	}
}

func TestDBQueryThreadChangeResetsCountersAndVerification(t *testing.T) {
	for _, exe := range []string{"/usr/sbin/mariadbd", "/usr/bin/unrelated"} {
		t.Run(exe, func(t *testing.T) {
			f := newDBQueryFixture(t)
			proc := &fakeDBProcfs{ticks: map[int]uint64{100: 1000, 200: 100000}, read: map[int]uint64{100: 1000, 200: 100000}}
			f.w.procfs = proc
			q := mariaQuery(7, 70, 600, "app")
			q.OSThreadID = 100
			f.queries = []checks.DBQuery{q}
			f.cycle()
			f.now = f.now.Add(time.Second)
			f.queries[0].OSThreadID, proc.exe = 200, exe
			f.cycle()
			got := checks.DBQueriesFromData(f.results[len(f.results)-1].Data)[0]
			if got.CPUReady || got.IOReady || proc.exeCalls != 2 {
				t.Fatalf("new thread reused counters or trust: %+v verifications=%d", got, proc.exeCalls)
			}
			f.now = f.now.Add(time.Second)
			proc.ticks[200] += 100
			proc.read[200] += 1024
			f.cycle()
			got = checks.DBQueriesFromData(f.results[len(f.results)-1].Data)[0]
			if exe == "/usr/sbin/mariadbd" {
				if !got.CPUReady || got.CPUThread != 100 || got.CPU != 12.5 || !got.IOReady || got.IORead != 1024 {
					t.Fatalf("new baseline did not measure the new thread: %+v", got)
				}
			} else if got.CPUReady || got.IOReady {
				t.Fatalf("unrelated thread was sampled: %+v", got)
			}
		})
	}
}

func TestDBQueryMissingThreadIDRequiresANewBaseline(t *testing.T) {
	f := newDBQueryFixture(t)
	proc := &fakeDBProcfs{ticks: map[int]uint64{100: 1000}}
	f.w.procfs = proc
	q := mariaQuery(7, 70, 600, "app")
	q.OSThreadID = 100
	f.queries = []checks.DBQuery{q}
	f.cycle()
	for _, tid := range []int64{0, 100} {
		f.now = f.now.Add(time.Second)
		proc.ticks[100] += 100
		f.queries[0].OSThreadID = tid
		f.cycle()
		if got := checks.DBQueriesFromData(f.results[len(f.results)-1].Data)[0]; got.CPUReady || got.IOReady {
			t.Fatalf("missing thread identity retained an old baseline: %+v", got)
		}
	}
	if proc.exeCalls != 2 {
		t.Fatalf("returning thread must be verified again: %d calls", proc.exeCalls)
	}
}
