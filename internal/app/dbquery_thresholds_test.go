package app

import (
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
)

func TestDBQueryResourceIncidentLifecycle(t *testing.T) {
	f := newDBQueryFixture(t)
	cfg, err := checks.ParseDBQueryConfig(map[string]any{
		"engine": "mariadb", "socket": "/run/mysqld/mysqld.sock",
		"cpu_thread": map[string]any{"op": ">", "value": "90%"},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.w.cfg = cfg
	proc := &fakeDBProcfs{ticks: map[int]uint64{4102: 1000}}
	f.w.procfs = proc
	q := mariaQuery(8, 80, 10, "app")
	q.OSThreadID = 4102
	f.queries = []checks.DBQuery{q}
	for _, tt := range []struct {
		name        string
		ticks       uint64
		missing     bool
		ended       bool
		wantEvent   string
		wantUnknown bool
		wantAlerted bool
	}{
		{name: "baseline", ticks: 1000, wantUnknown: true},
		{name: "hot", ticks: 1100, wantEvent: eventKindFiring, wantAlerted: true},
		{name: "still hot", ticks: 1200, wantAlerted: true},
		{name: "missing sample holds incident", missing: true, wantUnknown: true, wantAlerted: true},
		{name: "baseline after error", ticks: 1300, wantUnknown: true, wantAlerted: true},
		{name: "cooled down", ticks: 1350, wantEvent: eventKindRecovered},
		{name: "new hot episode", ticks: 1450, wantEvent: eventKindFiring, wantAlerted: true},
		{name: "statement ended", ended: true, wantEvent: eventKindRecovered},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f.now = f.now.Add(time.Second)
			proc.ticks[4102] = tt.ticks
			if tt.missing {
				delete(proc.ticks, 4102)
			}
			if tt.ended {
				f.queries = nil
			}
			events := f.cycle()
			if tt.wantEvent == "" {
				if len(events) != 0 {
					t.Fatalf("unexpected events: %+v", events)
				}
			} else if len(events) != 1 || events[0].Kind != tt.wantEvent {
				t.Fatalf("events = %+v, want %s", events, tt.wantEvent)
			}
			if tt.wantEvent == eventKindFiring && !strings.Contains(events[0].Message, "cpu_thread 100% > 90%") {
				t.Fatalf("fire must name the measured threshold: %s", events[0].Message)
			}
			result := f.results[len(f.results)-1]
			if result.Unavailable != tt.wantUnknown || result.OK != (!tt.wantUnknown && !tt.wantAlerted) {
				t.Fatalf("result = %+v", result)
			}
			listed := checks.DBQueriesFromData(result.Data)
			if !tt.ended && (len(listed) != 1 || listed[0].Alerted != tt.wantAlerted || listed[0].Long) {
				t.Fatalf("resource incident must retain its state without claiming duration: %+v", listed)
			}
		})
	}
}

func TestDBQueryResourceRestoreAndBuilderSafety(t *testing.T) {
	f := newDBQueryFixture(t)
	cfg, err := checks.ParseDBQueryConfig(map[string]any{"engine": "mariadb", "cpu": map[string]any{"op": ">", "value": 10}})
	if err != nil {
		t.Fatal(err)
	}
	f.w.cfg = cfg
	q := mariaQuery(8, 80, 10, "app")
	q.Alerted = true
	f.w.restore = func() []checks.DBQuery { return []checks.DBQuery{q} }
	f.queries = []checks.DBQuery{mariaQuery(8, 80, 11, "app")}
	if events := f.cycle(); len(events) != 0 {
		t.Fatalf("unknown reading after restart must neither re-fire nor recover: %+v", events)
	}
	if listed := checks.DBQueriesFromData(f.results[len(f.results)-1].Data); !listed[0].Alerted {
		t.Fatalf("restored incident was lost: %+v", listed)
	}
	_, err = parseKillQuery(map[string]any{"then": map[string]any{"kill_query": map[string]any{"after": "30m", "users": []any{"app"}}}}, cfg)
	if err == nil || !strings.Contains(err.Error(), "resource thresholds") {
		t.Fatalf("builder must refuse a resource-based automatic kill: %v", err)
	}
}

func TestDBQueryResourceSummaryAndHookReadiness(t *testing.T) {
	w := &dbQueryWatcher{check: map[string]any{
		"summary": "CPU ${cpu}% / thread ${cpu_thread}% / memory ${memory}",
		"cpu":     map[string]any{"op": ">", "value": 10},
	}}
	for _, tt := range []struct {
		name    string
		query   checks.DBQuery
		summary string
		cpu     string
		thread  string
		memory  string
	}{
		{name: "unknown is not zero or config", summary: "CPU unavailable% / thread unavailable% / memory unavailable"},
		{name: "measured idle", query: checks.DBQuery{CPUReady: true, MemoryReady: true}, summary: "CPU 0% / thread 0% / memory 0", cpu: "0.00", thread: "0.00", memory: "0"},
		{name: "measured load", query: checks.DBQuery{CPU: 12.5, CPUThread: 100, CPUReady: true, MemoryBytes: 4096, MemoryReady: true}, summary: "CPU 12.5% / thread 100% / memory 4,096", cpu: "12.50", thread: "100.00", memory: "4096"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := w.summaryMessage(tt.query, "fallback"); got != tt.summary {
				t.Fatalf("summary = %q, want %q", got, tt.summary)
			}
			env := w.env(tt.query, dbQueryChangeThreshold, "threshold crossed")
			for key, want := range map[string]string{sermoEnvCPU: tt.cpu, sermoEnvCPUThread: tt.thread, sermoEnvMemory: tt.memory} {
				got, present := env[key]
				if got != want || present != (want != "") {
					t.Fatalf("%s = %q (%v), want %q", key, got, present, want)
				}
			}
		})
	}
}
