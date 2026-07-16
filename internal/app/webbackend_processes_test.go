package app

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
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
	cfg, err := config.Load(globalPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
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
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "self.pid")
	if err := os.WriteFile(pidfile, []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := writeWebProcessConfig(t, pidfile)

	wb, warnings := NewWebBackend(t.Context(), cfg, Deps{Backend: servicemgr.BackendSystemd, Manager: fakeManager{}, ExecxRunner: execx.CommandRunner{}})
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
		if p.PID == os.Getpid() {
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
		t.Fatalf("processes = %+v, want pid %d", detail.Processes, os.Getpid())
	}
}

func TestWebBackendDetailProcessesNone(t *testing.T) {
	cfg := writeWebProcessConfig(t, "/nonexistent/pidfile.pid")
	wb, _ := NewWebBackend(t.Context(), cfg, Deps{Backend: servicemgr.BackendSystemd, Manager: fakeManager{}, ExecxRunner: execx.CommandRunner{}})

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

	selectors, warnings := serviceProcessSelectors(context.Background(), map[string]any{}, deps, "web.service")
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

func TestServiceProcessSelectorsExplicitEmptySkipsInitDerivation(t *testing.T) {
	pidfile := filepath.Join(t.TempDir(), "web.pid")
	deps := Deps{
		Backend:     servicemgr.BackendSystemd,
		ExecxRunner: procInfoRunner{pidfile: pidfile},
	}

	selectors, warnings := serviceProcessSelectors(context.Background(), map[string]any{"processes": map[string]any{}}, deps, "web.service")
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
		ExecxRunner: procInfoRunner{},
	}
	tree := map[string]any{}

	selectors, warnings := serviceProcessSelectors(context.Background(), tree, deps, "wait-online.service")
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v, want none", warnings)
	}
	if len(selectors) != 0 {
		t.Fatalf("selectors = %+v, want none", selectors)
	}
	if !serviceNoResidentProcess(tree, selectors, serviceBackendPIDs(t.Context(), deps, "wait-online.service")) {
		t.Fatal("service without selectors or backend PIDs must be treated as no resident process")
	}
}

func TestServiceNoResidentProcessKeepsBackendPIDServiceResident(t *testing.T) {
	deps := Deps{
		Backend:     servicemgr.BackendSystemd,
		ExecxRunner: systemdPIDRunner{pid: os.Getpid()},
	}

	if serviceNoResidentProcess(map[string]any{}, nil, serviceBackendPIDs(t.Context(), deps, "web.service")) {
		t.Fatal("service with backend PIDs must not be treated as no resident process")
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
	if services[0].CPUReady || services[0].NumCPU != 0 || services[0].ProcessCount != 0 || services[0].RSS != 0 {
		t.Fatalf("service list runtime = cpuReady:%v numCPU:%d processes:%d rss:%d, want empty", services[0].CPUReady, services[0].NumCPU, services[0].ProcessCount, services[0].RSS)
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
	snaps.Publish("wait-online", map[string]checks.Result{
		"state": {Check: "state", OK: true},
	}, map[string]bool{"state": true})
	observability := NewObservabilityRegistry()
	observability.MarkReady("wait-online", time.Now())
	wb, warnings := NewWebBackend(t.Context(), cfg, Deps{
		Backend:       servicemgr.BackendSystemd,
		Manager:       fakeManager{},
		ExecxRunner:   procInfoRunner{},
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
	if !got.CPUReady || !got.CPUThreadReady {
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
	serviceMetrics := NewServiceMetricSampler()
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
	_, checkDeps, _ := serviceRuntime(t.Context(), "node_exporter", "node_exporter.service", map[string]any{}, Deps{
		Backend:          servicemgr.BackendSystemd,
		Manager:          fakeManager{},
		Runtime:          t.TempDir(),
		DefaultTimeout:   time.Second,
		OperationTimeout: time.Second,
		ExecxRunner:      systemdPIDRunner{pid: os.Getpid()},
		Emit:             func(Event) {},
	}, nil, nil)
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

func TestWebBackendDetailIncludesProcessSelectorWarnings(t *testing.T) {
	cfg := writeWebServiceConfig(t, "web.yml", `
name: web
service: web
processes:
  main: {}
`)
	wb, warnings := NewWebBackend(t.Context(), cfg, Deps{Backend: servicemgr.BackendOpenRC, Manager: fakeManager{}, ExecxRunner: execx.CommandRunner{}})
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
	pidfile string
}

func (r procInfoRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	if name != "systemctl" {
		return execx.Result{}, nil
	}
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "-p PIDFile"):
		return execx.Result{Stdout: r.pidfile + "\n"}, nil
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
	hz      float64
	ncpu    int
}

func (r *liveCPUReader) ProcessCPU(pid int) (uint64, bool) {
	v, ok := r.cpu[pid]
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
func (*liveCPUReader) TotalMemory() (uint64, uint64, bool) { return 0, 0, false }
func (*liveCPUReader) SystemCPU() (uint64, uint64, bool)   { return 0, 0, false }
func (*liveCPUReader) LoadAverages() (float64, float64, float64, bool) {
	return 0, 0, 0, false
}
func (r *liveCPUReader) NumCPU() int         { return r.ncpu }
func (r *liveCPUReader) ClockTicks() float64 { return r.hz }
