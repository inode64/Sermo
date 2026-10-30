package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/control"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/metrics"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// writeWebServiceConfig writes a global config with a single services dir plus one
// service file (serviceFile under services/, holding body), loads it, and returns
// the resulting config.
func writeWebServiceConfig(t *testing.T, serviceFile, body string) *config.Config {
	t.Helper()
	root := t.TempDir()
	enabled := filepath.Join(root, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(globalPath, []byte(`
paths:
  services: [`+enabled+`]
defaults:
  policy:
    cooldown: 5m
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(enabled, serviceFile), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadTestConfig(t, globalPath)
	return cfg
}

func writeWebProcessConfig(t *testing.T, pidfile string) *config.Config {
	t.Helper()
	return writeWebServiceConfig(t, "mysql-main.yml", `
name: mysql-main
service: mysql
pidfile: `+pidfile+`
`)
}

func TestWebBackendDetailProcessesRealPidfile(t *testing.T) {
	const daemonPID = 4242
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "mysqld.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(daemonPID)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := writeWebProcessConfig(t, pidfile)
	procs := serviceOwnershipReader{daemonPID: {
		PID: daemonPID, PPID: 1, StartTicks: 77, StartTicksOK: true, UID: 27, Exe: "/usr/sbin/mysqld", ExeOK: true,
		Cgroup: "0::/system.slice/mysql.service",
	}}

	wb, warnings := NewWebBackend(t.Context(), cfg, Deps{Backend: servicemgr.BackendSystemd, Manager: fakeManager{}, ProcReader: procs, ExecxRunner: &execxtest.Runner{}})
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}

	detail, ok := wb.Detail(context.Background(), "mysql-main")
	if !ok {
		t.Fatal("detail not found")
	}
	if len(detail.Processes) == 0 {
		t.Fatal("expected at least one process from pidfile")
	}
	found := false
	for _, p := range detail.Processes {
		if p.PID == daemonPID {
			found = true
			if p.Source != "pidfile" {
				t.Fatalf("self process source = %q, want pidfile", p.Source)
			}
			if p.Role != "pidfile" {
				t.Fatalf("self process role = %q, want pidfile", p.Role)
			}
		}
	}
	if !found {
		t.Fatalf("processes = %+v, want pid %d", detail.Processes, daemonPID)
	}
}

func TestWebBackendDetailProcessesNone(t *testing.T) {
	cfg := writeWebProcessConfig(t, "/nonexistent/pidfile.pid")
	wb, _ := NewWebBackend(t.Context(), cfg, Deps{Backend: servicemgr.BackendSystemd, Manager: fakeManager{}, ExecxRunner: &execxtest.Runner{}})

	detail, ok := wb.Detail(context.Background(), "mysql-main")
	if !ok {
		t.Fatal("detail not found")
	}
	if detail.Processes != nil {
		t.Fatalf("processes = %+v, want nil/empty", detail.Processes)
	}
	if len(detail.ProcessWarnings) != 1 {
		t.Fatalf("ProcessWarnings = %+v, want 1 warning", detail.ProcessWarnings)
	}
	if !strings.Contains(detail.ProcessWarnings[0], "/nonexistent/pidfile.pid") {
		t.Fatalf("ProcessWarnings[0] = %q, want pidfile path", detail.ProcessWarnings[0])
	}
}

func TestInitDerivedProcessSelectors(t *testing.T) {
	tests := []struct {
		name string
		info servicemgr.ProcInfo
		want process.Selector
	}{
		{
			name: "pidfile",
			info: servicemgr.ProcInfo{Pidfile: "/run/app.pid", Exe: "/usr/bin/app", User: "app"},
			want: process.Selector{Name: "init", Type: process.SelectorPidfile, Paths: []string{"/run/app.pid"}},
		},
		{
			name: "cmd with user",
			info: servicemgr.ProcInfo{Cmd: `(^|[[:space:]])/usr/bin/app($|[[:space:]])`, User: "app"},
			want: process.Selector{Name: "init", Type: process.SelectorCommandMatch, Cmd: `(^|[[:space:]])/usr/bin/app($|[[:space:]])`, User: "app"},
		},
		{
			name: "exe with user",
			info: servicemgr.ProcInfo{Exe: "/usr/bin/app", User: "app"},
			want: process.Selector{Name: "init", Type: process.SelectorCommandMatch, Exe: "/usr/bin/app", User: "app"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := initDerivedProcessSelectors(tc.info)
			if len(got) != 1 {
				t.Fatalf("selectors = %+v, want one", got)
			}
			if got[0].Name != tc.want.Name || got[0].Type != tc.want.Type || got[0].Exe != tc.want.Exe || got[0].Cmd != tc.want.Cmd || got[0].User != tc.want.User || strings.Join(got[0].Paths, ",") != strings.Join(tc.want.Paths, ",") {
				t.Fatalf("selector = %+v, want %+v", got[0], tc.want)
			}
		})
	}
}

func TestInitDerivedProcessSelectorsRequireUserForCommandMatch(t *testing.T) {
	for _, info := range []servicemgr.ProcInfo{
		{Exe: "/usr/bin/app"},
		{Cmd: `(^|[[:space:]])/usr/bin/app($|[[:space:]])`},
	} {
		if got := initDerivedProcessSelectors(info); len(got) != 0 {
			t.Fatalf("selectors = %+v, want none without user", got)
		}
	}
}

func TestServiceProcessSelectorsDerivesInitPidfile(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "web.pid")
	deps := Deps{
		Backend:     servicemgr.BackendSystemd,
		ExecxRunner: procInfoRunner{pidfile: pidfile},
	}

	selectors, warnings, _ := serviceProcessSelectors(context.Background(), map[string]any{}, deps, "web.service", false)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(selectors) != 1 {
		t.Fatalf("selectors = %+v, want one pidfile selector", selectors)
	}
	if selectors[0].Type != process.SelectorPidfile || strings.Join(selectors[0].Paths, ",") != pidfile {
		t.Fatalf("selector = %+v, want pidfile %s", selectors[0], pidfile)
	}
}

// TestServiceProcessSelectorsDetectOncePerGeneration: the workers build and
// the web backend build derive the same service's init process identity
// back-to-back; the generation's target cache must probe the backend once.
func TestServiceProcessSelectorsDetectOncePerGeneration(t *testing.T) {
	runner := &probeCountingRunner{Runner: procInfoRunner{pidfile: filepath.Join(t.TempDir(), "web.pid")}}
	deps := Deps{Backend: servicemgr.BackendSystemd, ExecxRunner: runner, Targets: control.NewTargetCache()}

	first, _, _ := serviceProcessSelectors(t.Context(), map[string]any{}, deps, "web.service", false)
	probes := runner.calls.Load()
	second, _, _ := serviceProcessSelectors(t.Context(), map[string]any{}, deps, "web.service", false)

	if probes == 0 || runner.calls.Load() != probes {
		t.Fatalf("backend probes = %d then %d, want one detection per generation", probes, runner.calls.Load())
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached selectors = %+v, want %+v", second, first)
	}
}

type probeCountingRunner struct {
	execx.Runner
	calls atomic.Int32
}

func (r *probeCountingRunner) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	r.calls.Add(1)
	return r.Runner.Run(ctx, name, args...)
}

func TestServiceProcessSelectorsExplicitEmptySkipsInitDerivation(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "web.pid")
	deps := Deps{
		Backend:     servicemgr.BackendSystemd,
		ExecxRunner: procInfoRunner{pidfile: pidfile},
	}

	selectors, warnings, _ := serviceProcessSelectors(context.Background(), map[string]any{"processes": map[string]any{}}, deps, "web.service", false)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(selectors) != 0 {
		t.Fatalf("selectors = %+v, want none for explicit empty processes", selectors)
	}
	if !noResidentProcess(map[string]any{"processes": map[string]any{}}) {
		t.Fatal("explicit empty processes must mark no resident process")
	}
}

func TestServiceNoResidentProcessInfersInitServiceWithoutPIDs(t *testing.T) {
	deps := Deps{
		Backend:     servicemgr.BackendSystemd,
		ExecxRunner: procInfoRunner{unitType: "oneshot"},
	}
	tree := map[string]any{}

	selectors, warnings, _ := serviceProcessSelectors(context.Background(), tree, deps, "wait-online.service", false)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(selectors) != 0 {
		t.Fatalf("selectors = %+v, want none", selectors)
	}
	unitKeepsProcess := func() bool {
		return servicemgr.UnitKeepsProcess(t.Context(), deps.ExecxRunner, deps.Backend, "wait-online.service")
	}
	if !serviceNoResidentProcess(tree, selectors,
		ServiceBackendPIDs(t.Context(), deps.Backend, "wait-online.service", deps.BackendPIDs, deps.ExecxRunner), deps.Backend, unitKeepsProcess) {
		t.Fatal("a oneshot service without selectors or backend PIDs must be treated as no resident process")
	}
}

// sermod reaches this judgment during boot, ahead of the units ordered after it:
// a long-running service with no PID yet must stay observed for the generation.
func TestServiceNoResidentProcessKeepsDaemonThatHasNotStartedYet(t *testing.T) {
	deps := Deps{Backend: servicemgr.BackendSystemd, ExecxRunner: procInfoRunner{unitType: "notify"}}

	if serviceNoResidentProcess(map[string]any{}, nil,
		ServiceBackendPIDs(t.Context(), deps.Backend, "openvpn-client@tun1.service", deps.BackendPIDs, deps.ExecxRunner), deps.Backend,
		func() bool {
			return servicemgr.UnitKeepsProcess(t.Context(), deps.ExecxRunner, deps.Backend, "openvpn-client@tun1.service")
		}) {
		t.Fatal("a daemon that has not started yet must not be judged processless")
	}
}

func TestServiceNoResidentProcessKeepsBackendPIDServiceResident(t *testing.T) {
	deps := Deps{
		Backend:     servicemgr.BackendSystemd,
		ExecxRunner: systemdPIDRunner{pid: os.Getpid()},
	}

	if serviceNoResidentProcess(map[string]any{}, nil,
		ServiceBackendPIDs(t.Context(), deps.Backend, "web.service", deps.BackendPIDs, deps.ExecxRunner), deps.Backend, func() bool { return false }) {
		t.Fatal("service with backend PIDs must not be treated as no resident process")
	}
}

func TestServiceNoResidentProcessKeepsStoppedContainerResident(t *testing.T) {
	if serviceNoResidentProcess(map[string]any{}, nil, func() ([]int, error) { return nil, nil }, servicemgr.BackendDocker, func() bool { return false }) {
		t.Fatal("a container without a PID during reload must retain process observation after recovery")
	}
	if !serviceNoResidentProcess(map[string]any{process.SectionProcesses: map[string]any{}}, nil, nil, servicemgr.BackendDocker, func() bool { return true }) {
		t.Fatal("explicit empty processes must retain its configured meaning")
	}
}

func TestWebBackendDetailNoResidentProcess(t *testing.T) {
	cfg := writeWebServiceConfig(t, "firehol.yml", `
name: firehol
service: firehol
processes: {}
`)
	wb, warnings := NewWebBackend(t.Context(), cfg, Deps{
		Backend:     servicemgr.BackendSystemd,
		Manager:     fakeManager{},
		ExecxRunner: procInfoRunner{pidfile: filepath.Join(t.TempDir(), "firehol.pid")},
	})
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}

	detail, ok := wb.Detail(context.Background(), "firehol")
	if !ok {
		t.Fatal("detail not found")
	}
	if !detail.NoResidentProcess {
		t.Fatal("detail should mark firehol as no resident process")
	}
	if len(detail.Processes) != 0 || detail.ProcessTotals != nil || len(detail.ProcessWarnings) != 0 {
		t.Fatalf("process fields = processes:%+v totals:%+v warnings:%+v, want all empty", detail.Processes, detail.ProcessTotals, detail.ProcessWarnings)
	}
	services := wb.Services(context.Background())
	if len(services) != 1 {
		t.Fatalf("Services returned %d entries, want 1", len(services))
	}
	if !services[0].NoResidentProcess {
		t.Fatal("service list should mark firehol as no resident process")
	}
	if services[0].CPUReady || services[0].NumCPU != 0 || services[0].RSS != 0 {
		t.Fatalf("service list runtime = cpuReady:%v numCPU:%d rss:%d, want empty", services[0].CPUReady, services[0].NumCPU, services[0].RSS)
	}
	if runtime, ok := wb.ServiceRuntime(context.Background(), "firehol", time.Hour); ok {
		t.Fatalf("ServiceRuntime returned %+v for no resident process service", runtime)
	}
}

func TestWebBackendInitHelperWithoutPIDsDoesNotWaitForRuntimeMetrics(t *testing.T) {
	cfg := writeWebServiceConfig(t, "wait-online.yml", `
name: wait-online
service: NetworkManager-wait-online
checks:
  state:
    type: service
    expect: active
`)
	snaps := NewSnapshots()
	resolved, errs := cfg.Resolve("wait-online")
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	// The injected stale-binary and fds checks run on every service; with no
	// process to attribute they report nothing replaced and no descriptor use.
	snaps.publishConfigured("wait-online", map[string]checks.Result{
		"state":        {Check: "state", OK: true},
		"stale-binary": {Check: "stale-binary", OK: true, Reports: checks.ReportsState},
		"fds":          {Check: "fds", OK: true, Optional: true},
	}, map[string]bool{"state": true, "stale-binary": true, "fds": true},
		map[string]string{"state": checks.CheckTypeService, "stale-binary": checks.CheckTypeStaleBinary, "fds": checks.CheckTypeMetric}, serviceSnapshotConfigID(resolved.Tree))
	observability := NewObservabilityRegistry()
	observability.MarkReady("wait-online", time.Now())
	wb, warnings := NewWebBackend(t.Context(), cfg, Deps{
		Backend:       servicemgr.BackendSystemd,
		Manager:       fakeManager{},
		ExecxRunner:   procInfoRunner{unitType: "oneshot"},
		Snapshots:     snaps,
		Observability: observability,
	})
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}

	services := wb.Services(context.Background())
	if len(services) != 1 {
		t.Fatalf("Services returned %d entries, want 1", len(services))
	}
	if !services[0].NoResidentProcess {
		t.Fatal("service without process selectors or backend PIDs should be marked no resident process")
	}
	if services[0].State != TargetStateMonitored || !services[0].ObservabilityReady || len(services[0].ObservabilityMissing) != 0 {
		t.Fatalf("service = %+v, want monitored without missing runtime metrics", services[0])
	}
}

func TestWorkerLiveCPUUsesInitDerivedProcessSelectors(t *testing.T) {
	pid := os.Getpid()
	pidfile := filepath.Join(t.TempDir(), "web.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}

	clock := time.Unix(0, 0)
	reader := &liveCPUReader{cpu: map[int]uint64{pid: 0}, hz: 100, ncpu: 2}
	collector := metrics.New(reader)
	collector.Now = func() time.Time { return clock }
	live := NewLiveMetrics()

	w, _, warnings := buildWorker(t.Context(), "web", "web.service", map[string]any{}, Deps{
		Backend:          servicemgr.BackendSystemd,
		Manager:          fakeManager{},
		Runtime:          t.TempDir(),
		DefaultTimeout:   time.Second,
		OperationTimeout: time.Second,
		Live:             live,
		LiveCollector:    collector,
		ExecxRunner:      procInfoRunner{pidfile: pidfile},
		Now:              func() time.Time { return clock },
		Emit:             func(Event) {},
	}, nil)
	if len(warnings) != 0 {
		t.Fatalf("buildWorker warnings = %v, want none", warnings)
	}

	w.RunCycle(context.Background())
	clock = clock.Add(time.Second)
	reader.cpu[pid] = 50
	w.RunCycle(context.Background())

	got, ok := live.Get("web")
	if !ok {
		t.Fatal("live CPU sample not published")
	}
	if !got.CPUReady {
		t.Fatalf("live CPU not ready: %+v", got)
	}
	if pct := got.PerProcCPU[pid]; pct < 49.9 || pct > 50.1 {
		t.Fatalf("PerProcCPU[%d] = %v, want ~50", pid, pct)
	}
}

func TestWorkerRecordsServiceRuntimeMetricsForWebHistory(t *testing.T) {
	pid := os.Getpid()
	pidfile := filepath.Join(t.TempDir(), "web.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := writeWebProcessConfig(t, pidfile)

	clock := time.Unix(0, 0)
	reader := &liveCPUReader{
		cpu:     map[int]uint64{pid: 0},
		rss:     map[int]uint64{pid: 4096},
		ioRead:  map[int]uint64{pid: 1000},
		ioWrite: map[int]uint64{pid: 2000},
		hz:      100,
		ncpu:    2,
	}
	collector := metrics.New(reader)
	collector.Now = func() time.Time { return clock }
	serviceMetrics := NewServiceMetricSampler(nil)
	deps := Deps{
		Backend:          servicemgr.BackendSystemd,
		Manager:          fakeManager{},
		Runtime:          t.TempDir(),
		DefaultTimeout:   time.Second,
		OperationTimeout: time.Second,
		Live:             NewLiveMetrics(),
		LiveCollector:    collector,
		ServiceMetrics:   serviceMetrics,
		Collector:        collector,
		ExecxRunner:      procInfoRunner{pidfile: pidfile},
		Now:              func() time.Time { return clock },
		Emit:             func(Event) {},
	}
	workers, _, warnings := BuildWorkers(t.Context(), cfg, deps, collector)
	if len(warnings) != 0 {
		t.Fatalf("BuildWorkers warnings = %v, want none", warnings)
	}
	if len(workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(workers))
	}

	workers[0].RunCycle(context.Background())
	clock = clock.Add(time.Minute)
	reader.cpu[pid] = 100
	reader.rss[pid] = 8192
	reader.ioRead[pid] = 7000
	reader.ioWrite[pid] = 5000
	workers[0].RunCycle(context.Background())

	wb, warnings := NewWebBackend(t.Context(), cfg, deps)
	if len(warnings) != 0 {
		t.Fatalf("NewWebBackend warnings = %v, want none", warnings)
	}
	got, ok := wb.ServiceRuntime(context.Background(), "mysql-main", time.Hour)
	if !ok {
		t.Fatal("ServiceRuntime not found")
	}
	if got.Memory.Summary.Count == 0 || len(got.Memory.Points) == 0 {
		t.Fatalf("memory history empty: %+v", got.Memory)
	}
	if got.CPU.Summary.Count == 0 || len(got.CPU.Points) == 0 {
		t.Fatalf("CPU history empty: %+v", got.CPU)
	}
	if got.IO.Summary.Count == 0 || len(got.IO.Points) == 0 {
		t.Fatalf("IO history empty: %+v", got.IO)
	}
}

func TestServiceRuntimePidfileCheckUsesBackendFallbackWhenSystemdHasNoPIDFile(t *testing.T) {
	runtime := BuildServiceRuntime(t.Context(), ServiceRuntimeConfig{
		Service: "node_exporter", Unit: "node_exporter.service", Tree: map[string]any{},
		Deps: Deps{
			Backend:          servicemgr.BackendSystemd,
			Manager:          fakeManager{},
			Runtime:          t.TempDir(),
			DefaultTimeout:   time.Second,
			OperationTimeout: time.Second,
			ExecxRunner:      systemdPIDRunner{pid: os.Getpid()},
			Emit:             func(Event) {},
		},
	})
	checkDeps := runtime.CheckDeps
	built, warnings := checks.Build(map[string]any{
		"pidfile": map[string]any{"type": "pidfile", "path": filepath.Join(t.TempDir(), "missing.pid")},
	}, checkDeps)
	if len(warnings) != 0 {
		t.Fatalf("Build warnings = %v", warnings)
	}
	results := checks.Run(context.Background(), built, 1)
	if len(results) != 1 || !results[0].OK {
		t.Fatalf("pidfile result = %+v, want OK via backend fallback", results)
	}
}

// After a daemon-reload PID 1 can leave systemctl unanswered for seconds. A
// backend PID query that did not answer is not evidence of "no process": the
// pidfile check, its identity claim and the no-resident judgment must not read
// it as one.
func TestServiceRuntimeDoesNotReadAFailedBackendPIDQueryAsNoProcess(t *testing.T) {
	deps := Deps{
		Backend:          servicemgr.BackendSystemd,
		Manager:          fakeManager{},
		Runtime:          t.TempDir(),
		DefaultTimeout:   time.Second,
		OperationTimeout: time.Second,
		ExecxRunner:      stalledPIDRunner{},
		Emit:             func(Event) {},
	}
	pidfileResult := func(t *testing.T, tree map[string]any, path string) checks.Result {
		t.Helper()
		runtime := BuildServiceRuntime(t.Context(), ServiceRuntimeConfig{Service: "acpid", Unit: "acpid.service", Tree: tree, Deps: deps})
		built, warnings := checks.Build(map[string]any{"pidfile": map[string]any{"type": "pidfile", "path": path}}, runtime.CheckDeps)
		if len(warnings) != 0 {
			t.Fatalf("Build warnings = %v", warnings)
		}
		results := checks.Run(t.Context(), built, 1)
		if len(results) != 1 {
			t.Fatalf("results = %+v, want one", results)
		}
		return results[0]
	}

	t.Run("absent pidfile", func(t *testing.T) {
		res := pidfileResult(t, map[string]any{}, filepath.Join(t.TempDir(), "acpid.pid"))
		if res.OK || !res.Unavailable || !strings.Contains(res.Message, "MainPID") {
			t.Fatalf("pidfile result = %+v, want unavailable naming the failed query", res)
		}
	})
	t.Run("pid the selectors do not claim", func(t *testing.T) {
		pid := os.Getpid()
		pidfile := filepath.Join(t.TempDir(), "acpid.pid")
		if err := os.WriteFile(pidfile, []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		deps := deps
		deps.ProcReader = serviceOwnershipReader{pid: {PID: pid, PPID: 1, Exe: "/usr/bin/wrapper", ExeOK: true}}
		tree := map[string]any{"processes": map[string]any{"main": map[string]any{"exe": "/usr/sbin/acpid", "user": "root"}}}
		runtime := BuildServiceRuntime(t.Context(), ServiceRuntimeConfig{Service: "acpid", Unit: "acpid.service", Tree: tree, Deps: deps})
		if claimed, known := runtime.CheckDeps.PidfileClaim(pid); known {
			t.Fatalf("claim = %v/%v, want unknown while the backend does not answer", claimed, known)
		}
	})
	t.Run("no-resident judgment", func(t *testing.T) {
		runtime := BuildServiceRuntime(t.Context(), ServiceRuntimeConfig{Service: "acpid", Unit: "acpid.service", Tree: map[string]any{}, Deps: deps})
		if runtime.NoResidentProcess {
			t.Fatal("a service must not lose process observation because its PID query failed")
		}
	})
}

// An operation's observation is the evidence a stop or start acts on. When the
// backend does not answer, a leftover that only the unit's cgroup could name (a
// KillMode=process remnant running another executable) is invisible to the
// selectors, so absence is not proven and the observation must say so.
func TestOperationObservationReportsAFailedBackendPIDQuery(t *testing.T) {
	deps := Deps{
		Backend:          servicemgr.BackendSystemd,
		Manager:          fakeManager{},
		Runtime:          t.TempDir(),
		DefaultTimeout:   time.Second,
		OperationTimeout: time.Second,
		ExecxRunner:      stalledPIDRunner{},
		ProcReader:       serviceOwnershipReader{4242: {PID: 4242, PPID: 1, Exe: "/usr/bin/leftover", ExeOK: true}},
		Emit:             func(Event) {},
	}
	tree := map[string]any{"processes": map[string]any{"main": map[string]any{"exe": "/usr/sbin/acpid", "user": "root"}}}
	for _, targets := range []*control.TargetCache{nil, control.NewTargetCache()} {
		deps := deps
		deps.Targets = targets
		runtime := BuildServiceRuntime(t.Context(), ServiceRuntimeConfig{Service: "acpid", Unit: "acpid.service", Tree: tree, Deps: deps})
		observation, err := runtime.Discoverer.Observe(runtime.Selectors)
		if err == nil && observation.AbsenceKnown {
			t.Fatalf("target cache %v: observation = %+v, nil error; want absence unproven while the backend does not answer", targets != nil, observation)
		}
	}
}

// stalledPIDRunner answers systemd's static unit properties but fails the
// runtime PID queries, the way systemctl does while PID 1 is busy.
type stalledPIDRunner struct{}

func (stalledPIDRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	joined := strings.Join(args, " ")
	if name == "systemctl" && (strings.Contains(joined, "-p MainPID") || strings.Contains(joined, "-p ControlGroup")) {
		return execx.Result{ExitCode: -1}, context.DeadlineExceeded
	}
	return execx.Result{Stdout: "\n"}, nil
}

func TestWebBackendDetailIncludesProcessSelectorWarnings(t *testing.T) {
	cfg := writeWebServiceConfig(t, "web.yml", `
name: web
service: web
processes:
  main: {}
`)
	wb, warnings := NewWebBackend(t.Context(), cfg, Deps{Backend: servicemgr.BackendOpenRC, Manager: fakeManager{}, ExecxRunner: &execxtest.Runner{}})
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}
	detail, ok := wb.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	if len(detail.ProcessWarnings) != 1 || !strings.Contains(detail.ProcessWarnings[0], "requires exe or cmd") {
		t.Fatalf("ProcessWarnings = %+v, want process selector warning", detail.ProcessWarnings)
	}
}

type procInfoRunner struct {
	pidfile  string
	unitType string
}

func (r procInfoRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	if name != "systemctl" {
		return execx.Result{}, nil
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "-p PIDFile"):
		return execx.Result{Stdout: r.pidfile + "\n"}, nil
	case strings.Contains(joined, "-p Type"):
		return execx.Result{Stdout: r.unitType + "\n"}, nil
	case strings.Contains(joined, "-p MainPID"):
		return execx.Result{Stdout: "0\n"}, nil
	default:
		return execx.Result{Stdout: "\n"}, nil
	}
}

type systemdPIDRunner struct {
	pid int
}

func (r systemdPIDRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	if name != "systemctl" {
		return execx.Result{}, nil
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "-p PIDFile"):
		return execx.Result{Stdout: "\n"}, nil
	case strings.Contains(joined, "-p ControlGroup"):
		return execx.Result{Stdout: "\n"}, nil
	case strings.Contains(joined, "-p MainPID"):
		return execx.Result{Stdout: strconv.Itoa(r.pid) + "\n"}, nil
	default:
		return execx.Result{Stdout: "\n"}, nil
	}
}

type liveCPUReader struct {
	cpu     map[int]uint64
	rss     map[int]uint64
	ioRead  map[int]uint64
	ioWrite map[int]uint64
	fds     map[int]uint64
	threads map[int]uint64
	// threadCPU is pid -> tid -> jiffies, for the busiest-thread (max_core) reading.
	threadCPU map[int]map[int]uint64
	hz        float64
	ncpu      int
}

func (r *liveCPUReader) ProcessCPU(pid int) (uint64, bool) {
	v, ok := r.cpu[pid]
	return v, ok
}

func (r *liveCPUReader) ProcessThreadCPU(pid int) (map[int]uint64, bool) {
	v, ok := r.threadCPU[pid]
	return v, ok
}

func (r *liveCPUReader) ProcessRSS(pid int) (uint64, bool) {
	v, ok := r.rss[pid]
	return v, ok
}
func (r *liveCPUReader) ProcessIO(pid int) (uint64, uint64, bool) {
	rd, rok := r.ioRead[pid]
	wr, wok := r.ioWrite[pid]
	return rd, wr, rok || wok
}
func (r *liveCPUReader) ProcessFDs(pid int) (uint64, bool) {
	v, ok := r.fds[pid]
	return v, ok
}
func (r *liveCPUReader) ProcessThreads(pid int) (uint64, bool) {
	v, ok := r.threads[pid]
	return v, ok
}
func (*liveCPUReader) SystemCPU() (uint64, uint64, bool) { return 0, 0, false }
func (*liveCPUReader) LoadAverages() (float64, float64, float64, bool) {
	return 0, 0, 0, false
}
func (r *liveCPUReader) NumCPU() int         { return r.ncpu }
func (r *liveCPUReader) ClockTicks() float64 { return r.hz }

func (*liveCPUReader) ProcessSwap(int) (uint64, bool)    { return 0, false }
func (*liveCPUReader) ProcessFDLimit(int) (uint64, bool) { return 0, false }
func (*liveCPUReader) MemoryTotals(_ time.Duration) metrics.MemoryTotals {
	return metrics.MemoryTotals{}
}

func TestIdleUnitStatusOnlyRewritesAnInactiveUnitWithLiveTrigger(t *testing.T) {
	const triggers = "systemctl show -p TriggeredBy --value -- virtlogd.service"
	deps := Deps{Backend: servicemgr.BackendSystemd, ExecxRunner: &execxtest.Runner{ByLine: map[string]execx.Result{
		triggers:                                 {Stdout: "virtlogd.socket\n"},
		"systemctl is-active -- virtlogd.socket": {Stdout: "active\n"},
	}}}
	for _, tc := range []struct {
		live servicemgr.Status
		want servicemgr.Status
	}{
		{servicemgr.StatusInactive, servicemgr.StatusIdle},
		{servicemgr.StatusActive, servicemgr.StatusActive},
		{servicemgr.StatusFailed, servicemgr.StatusFailed},
	} {
		status := idleUnitStatus(deps, "virtlogd", func(context.Context) (servicemgr.Status, error) { return tc.live, nil })
		if got, err := status(t.Context()); err != nil || got != tc.want {
			t.Errorf("live %s: status = %s, %v, want %s", tc.live, got, err, tc.want)
		}
	}
	failed := idleUnitStatus(deps, "virtlogd", func(context.Context) (servicemgr.Status, error) {
		return servicemgr.StatusInactive, errors.New("timeout")
	})
	if _, err := failed(t.Context()); err == nil {
		t.Error("a failed query must stay an error")
	}
	if idleUnitStatus(deps, "virtlogd", nil) != nil {
		t.Error("a nil status must stay nil")
	}
}
