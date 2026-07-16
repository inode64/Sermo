package app

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
	"sermo/internal/web"
)

// TestServiceWatchesBuild builds a service tree's embedded watches: section and
// asserts a count watch becomes a scoped Watch named "<service>:<watch>" that
// fires (condition-style: OK) when the file-count threshold is met.
func TestServiceWatchesBuild(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b", "c"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tree := map[string]any{
		"watches": map[string]any{
			"queue-backlog": map[string]any{
				"check": map[string]any{
					"type":  "count",
					"path":  dir,
					"of":    "file",
					"count": map[string]any{"op": ">=", "value": 2},
				},
				"then": map[string]any{"notify": []any{"ops"}},
			},
		},
	}
	watches, warns := serviceWatches("mail", tree, checks.Deps{DefaultTimeout: time.Second}, nil, monitorTestDeps(), time.Minute)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("want 1 watch, got %d", len(watches))
	}
	w := watches[0]
	if w.Name != "mail:queue-backlog" {
		t.Errorf("name = %q, want mail:queue-backlog", w.Name)
	}
	if w.CheckType != "count" {
		t.Errorf("check type = %q, want count", w.CheckType)
	}
	if len(w.Notifiers) != 1 {
		t.Errorf("notifiers = %v, want one (ops)", w.Notifiers)
	}
	if r := w.Check.Run(context.Background()); !r.OK {
		t.Fatalf("3 files >= 2 should meet the threshold: %+v", r)
	}
}

// TestServiceWatchesProcessScoped proves a service watch runs with the service's
// scoped check deps: the process_count check invokes the injected ProcessCount
// closure with the watch's user, so it counts the service's processes only.
func TestServiceWatchesProcessScoped(t *testing.T) {
	var gotUser string
	checkDeps := checks.Deps{
		DefaultTimeout: time.Second,
		ProcessCount: func(user, _, _ string) int {
			gotUser = user
			return 7
		},
	}
	tree := map[string]any{
		"watches": map[string]any{
			"worker-runaway": map[string]any{
				"check": map[string]any{
					"type":  "process_count",
					"user":  "mailuser",
					"count": map[string]any{"op": ">", "value": 3},
				},
				"then": map[string]any{"notify": []any{"ops"}},
			},
		},
	}
	watches, warns := serviceWatches("mail", tree, checkDeps, nil, monitorTestDeps(), time.Minute)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("want 1 watch, got %d", len(watches))
	}
	if r := watches[0].Check.Run(context.Background()); !r.OK {
		t.Fatalf("7 > 3 should fire: %+v", r)
	}
	if gotUser != "mailuser" {
		t.Fatalf("scoped ProcessCount was called with user %q, want mailuser", gotUser)
	}
}

// TestServiceWatchesUnsupportedTypes checks the types rejected inside a service:
// the host-scoped multi-metric net/icmp/swap watches, and the `process` watch
// (host-wide match + kill) which is unsafe from a service scope.
func TestServiceWatchesUnsupportedTypes(t *testing.T) {
	cases := []struct{ typ, want string }{
		{"net", "host-scoped"},
		{"icmp", "host-scoped"},
		{"swap", "host-scoped"},
		{"process", "matches host-wide"},
	}
	for _, tc := range cases {
		t.Run(tc.typ, func(t *testing.T) {
			tree := map[string]any{
				"watches": map[string]any{
					"w": map[string]any{
						"check": map[string]any{"type": tc.typ, "name": "x"},
						"then":  map[string]any{"notify": []any{"ops"}},
					},
				},
			}
			watches, warns := serviceWatches("svc", tree, checks.Deps{}, nil, monitorTestDeps(), time.Minute)
			if len(watches) != 0 {
				t.Fatalf("%s watch should not build, got %d", tc.typ, len(watches))
			}
			if len(warns) != 1 || !strings.Contains(warns[0], tc.want) {
				t.Fatalf("warnings = %v, want one containing %q", warns, tc.want)
			}
		})
	}
}

// TestServiceWatchesMetric builds a metric watch: it reads a fresh per-watch
// metric source from the factory, and fires when the reading crosses the op.
func TestServiceWatchesMetric(t *testing.T) {
	built := 0
	src := func() checks.MetricReader {
		built++
		return func(scope, name string) (metrics.Reading, bool) {
			if scope == "service" && name == "cpu_thread" {
				return metrics.Reading{Percent: 95, HasPercent: true, Ready: true}, true
			}
			return metrics.Reading{}, false
		}
	}
	tree := map[string]any{
		"watches": map[string]any{
			"thread-hot": map[string]any{
				"check": map[string]any{"type": "metric", "scope": "service", "name": "cpu_thread", "op": ">", "value": "90%"},
				"then":  map[string]any{"notify": []any{"ops"}},
			},
		},
	}
	watches, warns := serviceWatches("app", tree, checks.Deps{DefaultTimeout: time.Second}, src, monitorTestDeps(), time.Minute)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("want 1 metric watch, got %d", len(watches))
	}
	if built != 1 {
		t.Errorf("metric source factory called %d times, want 1 (a dedicated collector per metric watch)", built)
	}
	if r := watches[0].Check.Run(context.Background()); !r.OK {
		t.Fatalf("cpu_thread 95 > 90 should fire: %+v", r)
	}
}

// TestServiceWatchMetricPrimesBeforeFiring proves a rate metric watch does not
// misfire on its first cycle (the dedicated collector has no delta yet, so the
// reading is not Ready) and fires only once the metric is Ready and crosses.
func TestServiceWatchMetricPrimesBeforeFiring(t *testing.T) {
	reads := 0
	src := func() checks.MetricReader {
		return func(scope, name string) (metrics.Reading, bool) {
			reads++
			return metrics.Reading{Percent: 95, HasPercent: true, Ready: reads > 1}, true
		}
	}
	tree := map[string]any{
		"watches": map[string]any{
			"thread-hot": map[string]any{
				"check": map[string]any{"type": "metric", "scope": "service", "name": "cpu_thread", "op": ">", "value": "90%"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/bin/true"}}},
			},
		},
	}
	watches, warns := serviceWatches("app", tree, checks.Deps{DefaultTimeout: time.Second}, src, monitorTestDeps(), time.Minute)
	if len(warns) != 0 || len(watches) != 1 {
		t.Fatalf("build: warns=%v watches=%d", warns, len(watches))
	}
	w := watches[0]
	var fires int
	w.Runner = HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
		fires++
		return nil
	})
	w.RunCycle(context.Background()) // first sample: not Ready -> must not fire
	if fires != 0 {
		t.Fatalf("first (unprimed) cycle must not fire, fired %d", fires)
	}
	w.RunCycle(context.Background()) // primed: 95%% > 90%% -> fire
	if fires != 1 {
		t.Fatalf("primed cycle over threshold should fire once, fired %d", fires)
	}
}

// TestServiceWatchesMetricNoSource warns when a metric watch is declared but no
// metric source factory is available.
func TestServiceWatchesMetricNoSource(t *testing.T) {
	tree := map[string]any{
		"watches": map[string]any{
			"m": map[string]any{
				"check": map[string]any{"type": "metric", "name": "memory", "op": ">", "value": "80%"},
				"then":  map[string]any{"notify": []any{"ops"}},
			},
		},
	}
	watches, warns := serviceWatches("svc", tree, checks.Deps{}, nil, monitorTestDeps(), time.Minute)
	if len(watches) != 0 {
		t.Fatalf("metric watch with no source should not build, got %d", len(watches))
	}
	if len(warns) != 1 || !strings.Contains(warns[0], "metric source unavailable") {
		t.Fatalf("warnings = %v, want one containing 'metric source unavailable'", warns)
	}
}

// TestServiceWatchesReservedNames rejects watch names that collide with the
// synthesized version/config monitors.
func TestServiceWatchesReservedNames(t *testing.T) {
	for _, name := range []string{"version", "config"} {
		tree := map[string]any{
			"watches": map[string]any{
				name: map[string]any{
					"check": map[string]any{"type": "count", "path": "/x", "count": map[string]any{"op": ">", "value": 1}},
					"then":  map[string]any{"notify": []any{"ops"}},
				},
			},
		}
		watches, warns := serviceWatches("svc", tree, checks.Deps{}, nil, monitorTestDeps(), time.Minute)
		if len(watches) != 0 {
			t.Errorf("%q watch should be rejected, got %d watches", name, len(watches))
		}
		if len(warns) != 1 || !strings.Contains(warns[0], "reserved") {
			t.Errorf("name %q: warnings = %v, want one containing 'reserved'", name, warns)
		}
	}
}

// TestBuildWorkerEmitsServiceWatches proves the worker builder threads a
// service's watches: section out as scoped Watch objects (the daemon appends
// these to the scheduler's watch set).
func TestBuildWorkerEmitsServiceWatches(t *testing.T) {
	dir := t.TempDir()
	tree := map[string]any{
		"processes": map[string]any{},
		"watches": map[string]any{
			"backlog": map[string]any{
				"check": map[string]any{"type": "count", "path": dir, "of": "file", "count": map[string]any{"op": ">=", "value": 0}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/bin/true"}}},
			},
		},
	}
	_, watches, warnings := buildWorker(t.Context(), "svc", "svc.service", tree, Deps{
		Manager:        fakeManager{},
		Runtime:        t.TempDir(),
		DefaultTimeout: time.Second,
		ExecxRunner:    execx.CommandRunner{},
		Now:            time.Now,
		Emit:           func(Event) {},
	}, nil)
	if len(warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", warnings)
	}
	if len(watches) != 1 {
		t.Fatalf("want 1 service watch, got %d", len(watches))
	}
	if watches[0].Name != "svc:backlog" {
		t.Errorf("watch name = %q, want svc:backlog", watches[0].Name)
	}
}

// TestWebBackendListsAndControlsServiceWatches proves a service's embedded
// watches appear in the web Watches list as "<service>:<watch>" and can be
// monitored/unmonitored like a host watch.
func TestWebBackendListsAndControlsServiceWatches(t *testing.T) {
	dir := t.TempDir()
	svcDir := filepath.Join(dir, "services")
	if err := os.MkdirAll(svcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	svc := "" +
		"name: svc\n" +
		"service: svc\n" +
		"policy: { cooldown: 1m }\n" +
		"watches:\n" +
		"  backlog:\n" +
		"    check: { type: count, path: /tmp, of: file, count: { op: \">=\", value: 0 } }\n" +
		"    then: { hook: { command: [\"/bin/true\"] } }\n"
	if err := os.WriteFile(filepath.Join(svcDir, "svc.yml"), []byte(svc), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(dir, "sermo.yml")
	if err := os.WriteFile(global, []byte("paths:\n  services: ["+svcDir+"]\n  runtime: /tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	snapshots := NewWatchSnapshots()
	snapshots.now = func() time.Time { return now }
	snapshots.Publish("svc:backlog", checks.CheckTypeCount, checks.Result{
		Check: "svc:backlog", OK: true, Condition: true, Message: "4 files",
		Data: map[string]any{
			checks.DataKeyPath:  "/tmp",
			checks.DataKeyOf:    "file",
			checks.DataKeyCount: 4,
		},
	})
	store := newFakeStore()
	b, warns := NewWebBackend(t.Context(), cfg, Deps{
		Backend: servicemgr.BackendSystemd, Manager: fakeManager{}, ExecxRunner: execx.CommandRunner{},
		DefaultTimeout: time.Second, Now: func() time.Time { return now }, Emit: func(Event) {}, Monitor: store,
		WatchSnapshots: snapshots,
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	var found *web.Watch
	for _, w := range b.Watches(context.Background()) {
		if w.Name == "svc:backlog" {
			ww := w
			found = &ww
		}
	}
	if found == nil {
		t.Fatal("service watch svc:backlog not listed in the web Watches panel")
	}
	if found.CheckType != "count" || !found.HasHook {
		t.Errorf("listed watch = %+v", found)
	}
	hasPathReading := false
	for _, reading := range found.Readings {
		if reading.Field == checks.DataKeyPath && reading.Value == "/tmp" {
			hasPathReading = true
			break
		}
	}
	if !hasPathReading {
		t.Errorf("service watch readings = %+v, want published count readings", found.Readings)
	}

	// It is controllable: unmonitor it via the same web path host watches use.
	if err := b.SetWatchMonitored(context.Background(), "svc:backlog", false); err != nil {
		t.Fatalf("SetWatchMonitored(svc:backlog): %v", err)
	}
	active, found2, err := store.Active(watchMonitorKey("svc:backlog"))
	if err != nil || !found2 || active {
		t.Fatalf("after unmonitor: active=%v found=%v err=%v; want active=false", active, found2, err)
	}
}

func TestWebBackendServiceWatchShowsSnapshotMeterAndReadings(t *testing.T) {
	now := time.Date(2026, 7, 16, 18, 0, 0, 0, time.UTC)
	snapshots := NewWatchSnapshots()
	snapshots.now = func() time.Time { return now }
	snapshots.Publish("svc:memory", checks.CheckTypeMemory, checks.Result{
		Check: "svc:memory", OK: true,
		Data: map[string]any{
			checks.DataKeyTotalBytes:     uint64(100),
			checks.DataKeyAvailableBytes: uint64(25),
			checks.DataKeyUsedPct:        75.0,
		},
	})
	b := &WebBackend{
		watchOrder:     []string{"svc:memory"},
		watches:        map[string]*webWatch{"svc:memory": {name: "svc:memory", checkType: checks.CheckTypeMemory, interval: time.Minute, serviceScoped: true}},
		watchSnapshots: snapshots,
		now:            func() time.Time { return now },
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("Watches() returned %d watches, want 1", len(watches))
	}
	if watches[0].Meter == nil || watches[0].Meter.UsedPct != 75 {
		t.Errorf("service watch meter = %+v, want 75%% used", watches[0].Meter)
	}
	if len(watches[0].Readings) == 0 {
		t.Errorf("service watch readings = %+v, want published readings", watches[0].Readings)
	}
}

// TestServiceWatchFiresHookViaRunCycle drives a built service watch through its
// real Watch.RunCycle and asserts the hook fires with the service-qualified name
// in the environment — the end-to-end runtime, not just Check.Run.
// buildServiceCountWatch builds a single "mail:backlog" service watch that fires a
// hook when dir holds >= 1 file, merging any extra keys (e.g. a "for" window) into
// the watch entry. It returns the built watch.
func buildServiceCountWatch(t *testing.T, dir string, extra map[string]any) *Watch {
	t.Helper()
	watch := map[string]any{
		"check": map[string]any{"type": "count", "path": dir, "of": "file", "count": map[string]any{"op": ">=", "value": 1}},
		"then":  map[string]any{"hook": map[string]any{"command": []any{"/bin/true"}}},
	}
	maps.Copy(watch, extra)
	tree := map[string]any{"watches": map[string]any{"backlog": watch}}
	watches, warns := serviceWatches("mail", tree, checks.Deps{DefaultTimeout: time.Second}, nil, monitorTestDeps(), time.Minute)
	if len(warns) != 0 || len(watches) != 1 {
		t.Fatalf("build: warns=%v watches=%d", warns, len(watches))
	}
	return watches[0]
}

func TestServiceWatchFiresHookViaRunCycle(t *testing.T) {
	dir := t.TempDir()
	for _, n := range []string{"a", "b"} {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	w := buildServiceCountWatch(t, dir, nil)
	var calls int
	var env map[string]string
	w.Runner = HookRunnerFunc(func(_ context.Context, _ []string, e map[string]string, _ time.Duration) error {
		calls++
		env = e
		return nil
	})
	w.RunCycle(context.Background())
	if calls != 1 {
		t.Fatalf("hook should fire once (2 files >= 1), got %d", calls)
	}
	if env["SERMO_WATCH"] != "mail:backlog" || env["SERMO_CHECK_TYPE"] != "count" {
		t.Fatalf("hook env = %v, want SERMO_WATCH=mail:backlog SERMO_CHECK_TYPE=count", env)
	}
}

// TestServiceWatchForWindow proves the for/within firing window applies to a
// service watch: it fires only after the condition holds for the required cycles.
func TestServiceWatchForWindow(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	w := buildServiceCountWatch(t, dir, map[string]any{"for": map[string]any{"cycles": 2}})
	var calls int
	w.Runner = HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error {
		calls++
		return nil
	})
	w.RunCycle(context.Background())
	if calls != 0 {
		t.Fatalf("for: {cycles: 2} must not fire on the first matching cycle, fired %d", calls)
	}
	w.RunCycle(context.Background())
	if calls != 1 {
		t.Fatalf("watch should fire on the second consecutive matching cycle, fired %d", calls)
	}
}

// TestServiceWatchesNone confirms a service with no watches: section yields
// nothing (no watches, no warnings).
func TestServiceWatchesNone(t *testing.T) {
	watches, warns := serviceWatches("svc", map[string]any{}, checks.Deps{}, nil, monitorTestDeps(), time.Minute)
	if len(watches) != 0 || len(warns) != 0 {
		t.Fatalf("empty tree = %d watches, %v warnings; want none", len(watches), warns)
	}
}
