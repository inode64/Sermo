package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/servicemgr"
	"sermo/internal/state"
	"sermo/internal/volume"
	web "sermo/internal/web"
)

func TestWebBackendEventsNilLog(t *testing.T) {
	b := &WebBackend{
		entries: map[string]*webEntry{"web": {}},
	}
	if got := b.Events(context.Background(), 10); got != nil {
		t.Fatalf("Events with nil log = %v, want nil", got)
	}
	events, ok := b.ServiceEvents(context.Background(), "web", 10)
	if !ok {
		t.Fatal("ServiceEvents should find configured service")
	}
	if events != nil {
		t.Fatalf("ServiceEvents with nil log = %v, want nil", events)
	}
	if _, ok := b.ServiceEvents(context.Background(), "missing", 10); ok {
		t.Fatal("unknown service must not be found")
	}
}

func TestWebBackendDetailRanFlag(t *testing.T) {
	snaps := NewSnapshots()
	snaps.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true, Message: "ok"},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				displayName: "web",
				checkNames:  []string{"fast", "slow"},
				checkTypes:  map[string]string{"fast": "tcp", "slow": "http"},
				status:      func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
		snapshots: snaps,
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	byName := map[string]bool{}
	for _, c := range detail.Checks {
		byName[c.Name] = c.Ran
	}
	if !byName["fast"] {
		t.Fatal("fast check should show ran=true in web detail")
	}
	if byName["slow"] {
		t.Fatal("interval-cached slow check should show ran=false in web detail")
	}
}

func TestWebBackendViewMonitorSource(t *testing.T) {
	at := time.Date(2026, 6, 7, 14, 0, 0, 0, time.UTC)
	store := newFakeStore()
	store.now = func() time.Time { return at }
	if err := store.SetActive("web", false, state.SourceCLI); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {unit: "nginx", backend: "systemd"},
		},
		store: store,
	}

	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.Monitored || svc.MonitorSource != state.SourceCLI {
		t.Fatalf("service = %+v", svc)
	}
	wantAt := at.UTC().Format(time.RFC3339)
	if svc.MonitorChangedAt != wantAt {
		t.Fatalf("MonitorChangedAt = %q, want %q", svc.MonitorChangedAt, wantAt)
	}
}

func TestWebBackendViewInterval(t *testing.T) {
	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {unit: "nginx", backend: "systemd", interval: 10 * time.Second},
		},
	}
	svc := b.view(context.Background(), "web", b.entries["web"])
	if svc.Interval != "10s" {
		t.Fatalf("Interval = %q, want %q", svc.Interval, "10s")
	}
}

func TestWebBackendWatchPolarityUsesSharedHealthTypes(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"autofs": map[string]any{"check": map[string]any{"type": "autofs"}},
			"count":  map[string]any{"check": map[string]any{"type": "count"}},
			"mysql":  map[string]any{"check": map[string]any{"type": "mysql"}},
			"ports":  map[string]any{"check": map[string]any{"type": "ports"}},
			"ws":     map[string]any{"check": map[string]any{"type": "ws"}},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	got := map[string]bool{}
	for _, w := range b.Watches(context.Background()) {
		got[w.Name] = w.FireOnFail
	}
	for _, name := range []string{"autofs", "mysql", "ports", "ws"} {
		if !got[name] {
			t.Fatalf("%s watch should be health-style: %v", name, got)
		}
	}
	if got["count"] {
		t.Fatalf("count watch should be condition-style: %v", got)
	}
}

func TestWebBackendWatchesExposeMonitorMode(t *testing.T) {
	store := newFakeStore()
	if err := store.SetActive(watchMonitorKey("disk-root"), false, state.SourceConfig); err != nil {
		t.Fatalf("SetActive: %v", err)
	}
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"disk-root": map[string]any{
				"monitor": config.MonitorDisabled,
				"check":   map[string]any{"type": "disk", "path": "/"},
			},
		},
	}}}

	b, warns := NewWebBackend(cfg, Deps{Monitor: store})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("got %d watches", len(watches))
	}
	if watches[0].Monitor != config.MonitorDisabled || watches[0].Monitored {
		t.Fatalf("watch monitor view = %+v", watches[0])
	}
}

func TestWebBackendStorageWatchIncludesFilesystemDetails(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"interval": "45s",
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data/app",
					"mounted":  true,
					"free_pct": map[string]any{"op": "<", "value": 15},
					"free_bytes": map[string]any{
						"op":    "<",
						"value": "10G",
					},
				},
				"then": map[string]any{
					"notify": []any{"ops", "pager"},
					"expand": map[string]any{"by": "5G"},
					"hook": map[string]any{
						"command": []any{"/usr/local/bin/sermo-disk-alert", "--path", "/data/app"},
					},
				},
			},
		},
	}}}
	usagePath := ""
	mountSampled := false
	b, warns := NewWebBackend(cfg, Deps{
		DiskUsage: func(path string) (checks.DiskStats, error) {
			usagePath = path
			return checks.DiskStats{
				UsedPct:       87.5,
				FreePct:       12.5,
				TotalBytes:    1000,
				FreeBytes:     125,
				InodesTotal:   100,
				InodesFree:    20,
				InodesUsedPct: 80,
				InodesFreePct: 20,
			}, nil
		},
		MountSampler: func() ([]checks.Mount, error) {
			mountSampled = true
			return []checks.Mount{
				{Device: "/dev/root", MountPoint: "/", FSType: "ext4", Options: []string{"rw"}},
				{Device: "/dev/mapper/data", MountPoint: "/data", FSType: "xfs", Options: []string{"rw", "noatime"}},
			}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 {
		t.Fatalf("got %d watches, want 1: %+v", len(watches), watches)
	}
	w := watches[0]
	if usagePath != "/data/app" || !mountSampled {
		t.Fatalf("samplers not called as expected: usagePath=%q mountSampled=%v", usagePath, mountSampled)
	}
	if w.Name != "storage-data" || w.Interval != "45s" || w.CheckType != "storage" {
		t.Fatalf("watch identity = %+v", w)
	}
	if !slices.Equal(w.Notifiers, []string{"ops", "pager"}) {
		t.Fatalf("notifiers = %v, want ops,pager", w.Notifiers)
	}
	if !slices.Equal(w.HookCommand, []string{"/usr/local/bin/sermo-disk-alert", "--path", "/data/app"}) {
		t.Fatalf("hook command = %v", w.HookCommand)
	}
	if w.Summary == "" || !strings.Contains(w.Summary, "/data/app") || !strings.Contains(w.Summary, "xfs") {
		t.Fatalf("summary = %q, want path and filesystem", w.Summary)
	}
	if len(w.Conditions) != 3 {
		t.Fatalf("conditions = %+v, want free_pct/free_bytes/mounted", w.Conditions)
	}
	cond := map[string]web.WatchCondition{}
	for _, c := range w.Conditions {
		cond[c.Field] = c
	}
	if cond["free_pct"].Op != "<" || cond["free_pct"].Value != "15" {
		t.Fatalf("free_pct condition = %+v", cond["free_pct"])
	}
	if cond["free_bytes"].Op != "<" || cond["free_bytes"].Value != "10G" {
		t.Fatalf("free_bytes condition = %+v", cond["free_bytes"])
	}
	if cond["mounted"].Op != "==" || cond["mounted"].Value != "true" {
		t.Fatalf("mounted condition = %+v", cond["mounted"])
	}
	if w.Disk == nil {
		t.Fatal("storage watch should include live filesystem info")
	}
	if w.Disk.MountPoint != "/data" || w.Disk.Device != "/dev/mapper/data" || w.Disk.FileSystem != "xfs" {
		t.Fatalf("disk mount info = %+v", w.Disk)
	}
	if w.Disk.FreeBytes != 125 || w.Disk.UsedBytes != 875 || w.Disk.FreePct != 12.5 || w.Disk.InodesFree != 20 {
		t.Fatalf("disk usage info = %+v", w.Disk)
	}
	if w.Expand == nil || w.Expand.ByBytes != 5<<30 {
		t.Fatalf("expand info = %+v, want 5G", w.Expand)
	}
}

func TestWebBackendNotifiersExposeEnabledState(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"notifiers": map[string]any{
			"muted": map[string]any{"enabled": false, "type": "slack", "webhook": "https://hooks.example/x"},
			"ops":   map[string]any{"type": "email", "dsn": "smtp://x", "from": "x@y", "to": []any{"a@b"}},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	got := b.Notifiers(context.Background())
	if len(got) != 2 {
		t.Fatalf("notifiers = %+v, want 2 entries", got)
	}
	byName := map[string]web.Notifier{}
	for _, n := range got {
		byName[n.Name] = n
	}
	if !byName["ops"].Enabled {
		t.Fatalf("ops should default enabled: %+v", byName["ops"])
	}
	if byName["muted"].Enabled {
		t.Fatalf("muted should be disabled: %+v", byName["muted"])
	}
}

func TestWebBackendExpandWatchUsesConfiguredPathAndSize(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data/app",
					"used_pct": map[string]any{"op": ">=", "value": 90},
				},
				"then": map[string]any{"expand": map[string]any{"by": "5G"}},
			},
		},
	}}}
	exp := &fakeExpander{res: volume.Result{VG: "vg0", LV: "data", GrewBytes: 5 << 30}}
	var events []Event
	b, warns := NewWebBackend(cfg, Deps{
		VolumeExpander:   exp,
		OperationTimeout: time.Second,
		Emit:             func(e Event) { events = append(events, e) },
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.ExpandWatch(context.Background(), "storage-data")
	if !res.OK {
		t.Fatalf("ExpandWatch failed: %+v", res)
	}
	if !slices.Equal(exp.calls, []string{"/data/app:5368709120"}) {
		t.Fatalf("expand calls = %v, want configured path and 5G", exp.calls)
	}
	if len(events) != 1 || events[0].Watch != "storage-data" || events[0].Kind != "expand" || events[0].Action != "expand" || events[0].Status != "ok" {
		t.Fatalf("events = %+v, want successful expand event", events)
	}
}

func TestWebBackendExpandWatchRejectsUnconfiguredAction(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data/app",
					"used_pct": map[string]any{"op": ">=", "value": 90},
				},
				"then": map[string]any{"notify": []any{"ops"}},
			},
		},
	}}}
	exp := &fakeExpander{res: volume.Result{VG: "vg0", LV: "data", GrewBytes: 5 << 30}}
	b, warns := NewWebBackend(cfg, Deps{VolumeExpander: exp, OperationTimeout: time.Second})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	res := b.ExpandWatch(context.Background(), "storage-data")
	if res.OK || !strings.Contains(res.Message, "no then.expand") {
		t.Fatalf("ExpandWatch = %+v, want missing expand rejection", res)
	}
	if len(exp.calls) != 0 {
		t.Fatalf("expand must not run without then.expand, calls=%v", exp.calls)
	}
}

func TestWebBackendStorageWatchReportsSamplerErrors(t *testing.T) {
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"watches": map[string]any{
			"storage-data": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/data",
					"free_pct": map[string]any{"op": "<", "value": 15},
				},
				"then": map[string]any{"notify": []any{"ops"}},
			},
		},
	}}}
	b, warns := NewWebBackend(cfg, Deps{
		DiskUsage: func(string) (checks.DiskStats, error) {
			return checks.DiskStats{}, errors.New("statfs failed")
		},
		MountSampler: func() ([]checks.Mount, error) {
			return nil, errors.New("mount table failed")
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}

	watches := b.Watches(context.Background())
	if len(watches) != 1 || watches[0].Disk == nil {
		t.Fatalf("watch disk info = %+v", watches)
	}
	if watches[0].Disk.SampleError != "statfs failed" || watches[0].Disk.MountSampleError != "mount table failed" {
		t.Fatalf("disk errors = %+v", watches[0].Disk)
	}
	if !strings.Contains(watches[0].Summary, "statfs failed") {
		t.Fatalf("summary = %q, want statfs error", watches[0].Summary)
	}
}

func TestWebBackendDetailAtTimestamp(t *testing.T) {
	t0 := time.Date(2026, 6, 7, 12, 30, 0, 0, time.UTC)
	t1 := t0.Add(time.Minute)
	snaps := NewSnapshots()
	snaps.now = func() time.Time { return t0 }
	snaps.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true},
	}, map[string]bool{"fast": true, "slow": true})

	snaps.now = func() time.Time { return t1 }
	snaps.Publish("web", map[string]checks.Result{
		"fast": {Check: "fast", OK: true},
		"slow": {Check: "slow", OK: true, Message: "cached"},
	}, map[string]bool{"fast": true})

	b := &WebBackend{
		order: []string{"web"},
		entries: map[string]*webEntry{
			"web": {
				checkNames: []string{"fast", "slow", "new"},
				checkTypes: map[string]string{"fast": "tcp", "slow": "http", "new": "command"},
			},
		},
		snapshots: snaps,
	}

	detail, ok := b.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	byName := map[string]struct {
		ran bool
		at  string
	}{}
	for _, c := range detail.Checks {
		byName[c.Name] = struct {
			ran bool
			at  string
		}{c.Ran, c.At}
	}
	wantT1 := t1.UTC().Format(time.RFC3339)
	wantT0 := t0.UTC().Format(time.RFC3339)
	if !byName["fast"].ran || byName["fast"].at != wantT1 {
		t.Fatalf("fast = %+v, want ran=true at=%s", byName["fast"], wantT1)
	}
	if byName["slow"].ran || byName["slow"].at != wantT0 {
		t.Fatalf("cached slow = %+v, want ran=false at=%s", byName["slow"], wantT0)
	}
	if byName["new"].ran || byName["new"].at != "" {
		t.Fatalf("unobserved new = %+v", byName["new"])
	}
}

func TestWebBackendIncludesDisabledServices(t *testing.T) {
	// NewWebBackend (and thus the web list) must include services that have
	// `enabled: false` so they are visible in the dashboard for the operator
	// to know they exist and can be activated (by editing config + reload).
	b := &WebBackend{
		order: []string{"mysql", "web"},
		entries: map[string]*webEntry{
			"mysql": {displayName: "MySQL", unit: "mysqld", backend: "systemd", disabled: true},
			"web": {
				displayName: "Web",
				unit:        "nginx",
				backend:     "systemd",
				status:      func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
			},
		},
	}

	svcs := b.Services(context.Background())
	if len(svcs) != 2 {
		t.Fatalf("got %d services, want 2 (including the disabled one)", len(svcs))
	}
	byName := map[string]web.Service{}
	for _, s := range svcs {
		byName[s.Name] = s
	}

	dis := byName["mysql"]
	if dis.Enabled || dis.Status != "disabled" || dis.State != TargetStateDisabled || dis.Monitored {
		t.Fatalf("disabled service = %+v, want Enabled=false, Status=disabled, State=disabled, Monitored=false", dis)
	}

	en := byName["web"]
	if !en.Enabled || en.Status == "disabled" || en.State != TargetStateMonitorized {
		t.Fatalf("normal service = %+v, want Enabled=true", en)
	}

	// Detail for disabled should succeed (returns basic info) but have no checks etc.
	d, ok := b.Detail(context.Background(), "mysql")
	if !ok || d.Name != "mysql" || d.Enabled {
		t.Fatalf("detail for disabled: ok=%v d=%+v", ok, d)
	}
	if len(d.Checks) != 0 {
		t.Fatalf("disabled detail should not expose checks")
	}

	// Operate must be rejected for disabled
	res := b.Operate(context.Background(), "mysql", "restart")
	if res.OK {
		t.Fatal("operate on disabled must fail")
	}
	if res.Message == "" || !strings.Contains(res.Message, "disabled") {
		t.Fatalf("operate error message should mention disabled: %q", res.Message)
	}

	// Preflight on disabled should not be found (no engine)
	if _, ok := b.Preflight(context.Background(), "mysql"); ok {
		t.Fatal("preflight on disabled should not succeed")
	}
}

func TestWebBackendConfigRenderAndDiff(t *testing.T) {
	root := t.TempDir()
	daemons := filepath.Join(root, "daemons")
	enabled := filepath.Join(root, "enabled")
	if err := os.MkdirAll(daemons, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "sermo.yml")
	daemonPath := filepath.Join(daemons, "web-daemon.yml")
	basePath := filepath.Join(enabled, "base.yml")
	webPath := filepath.Join(enabled, "web.yml")
	if err := os.WriteFile(globalPath, []byte(`
paths:
  daemons: [`+daemons+`]
  includes: [`+enabled+`]
defaults:
  policy:
    cooldown: 5m
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(daemonPath, []byte(`
kind: daemon
name: web-daemon
checks:
  tcp:
    type: tcp
    host: 127.0.0.1
    port: 80
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(basePath, []byte(`
kind: service
name: base
uses: web-daemon
service: base.service
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(webPath, []byte(`
kind: service
name: web
uses: web-daemon
service: web.service
checks:
  tcp:
    port: 8080
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(globalPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	b, warnings := NewWebBackend(cfg, Deps{Backend: "systemd", Manager: fakeManager{}, ExecxRunner: execx.CommandRunner{}})
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}

	rendered, ok, err := b.ConfigRender(context.Background(), "web", "yaml")
	if err != nil || !ok {
		t.Fatalf("ConfigRender: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(rendered.Content, "web.service") || !strings.Contains(rendered.Content, "8080") {
		t.Fatalf("rendered content missing resolved values:\n%s", rendered.Content)
	}
	wantSources := []string{globalPath, daemonPath, webPath}
	for _, want := range wantSources {
		if !slices.Contains(rendered.SourceFiles, want) {
			t.Fatalf("source files = %v, missing %s", rendered.SourceFiles, want)
		}
	}

	diff, ok, err := b.ConfigDiff(context.Background(), "base", "web")
	if err != nil || !ok {
		t.Fatalf("ConfigDiff: ok=%v err=%v", ok, err)
	}
	if diff.Identical || len(diff.Removed) == 0 || len(diff.Added) == 0 {
		t.Fatalf("diff = %+v, want changed lines", diff)
	}
	if _, ok, err := b.ConfigRender(context.Background(), "missing", "yaml"); err != nil || ok {
		t.Fatalf("missing render: ok=%v err=%v", ok, err)
	}
}

// fakeEnvRunnerForWeb is used to inject a custom execx runner via Deps.ExecxRunner
// and verify that hooks in watches built for the web backend receive the expected env.
type fakeEnvRunnerForWeb struct {
	calls []struct {
		env  []string
		name string
		args []string
	}
}

func (f *fakeEnvRunnerForWeb) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return execx.Result{}, nil
}
func (f *fakeEnvRunnerForWeb) RunEnv(ctx context.Context, env []string, name string, args ...string) (execx.Result, error) {
	f.calls = append(f.calls, struct {
		env  []string
		name string
		args []string
	}{env, name, args})
	return execx.Result{ExitCode: 0}, nil
}

func TestWebBackendPropagatesCustomExecxRunnerToWatchHooks(t *testing.T) {
	// Minimal config with a watch that has a hook. The check is a simple command that always succeeds.
	cfgContent := `
paths:
  daemons: []
  includes: []
  runtime: /tmp
defaults:
  policy:
    cooldown: 1m
watches:
  test-hook-watch:
    check:
      type: command
      command: ["/bin/true"]
    then:
      hook:
        command: ["/bin/custom-web-hook", "web-alert"]
`
	globalPath := filepath.Join(t.TempDir(), "sermo.yml")
	if err := os.WriteFile(globalPath, []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(globalPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}

	fake := &fakeEnvRunnerForWeb{}
	deps := Deps{
		Backend:        "systemd",
		Manager:        fakeManager{},
		ExecxRunner:    fake,
		DefaultTimeout: time.Second,
		Now:            time.Now,
		Emit:           func(Event) {},
	}

	// NewWebBackend will internally call BuildWatches with the deps, propagating ExecxRunner
	// to the OSHookRunner for the watch.
	_, warnings := NewWebBackend(cfg, deps)
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}

	// To exercise the hook, we simulate a watch cycle. Since watches are built internally,
	// we use BuildWatches directly with same deps to get the watch and run it (this mirrors
	// what NewWebBackend does for watch part).
	watches, wwarns := BuildWatches(cfg, deps, 30*time.Second)
	if len(wwarns) != 0 || len(watches) != 1 {
		t.Fatalf("expected 1 watch, warnings=%v", wwarns)
	}
	w := watches[0]
	// Command check is health-style (FireOnFail=true by default), so success (OK=true) means !fired.
	// Override to force fire on success for this test (we only care about runner being called with env).
	w.FireOnFail = false
	w.RunCycle(context.Background())

	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call to custom execx runner from web backend watch hook, got %d", len(fake.calls))
	}
	call := fake.calls[0]
	if call.name != "/bin/custom-web-hook" || len(call.args) != 1 || call.args[0] != "web-alert" {
		t.Fatalf("wrong argv to custom runner: %s %v", call.name, call.args)
	}
	// Verify specific env from the watch (SERMO_WATCH and data from the command check result if any, plus type)
	hasWatch := false
	hasType := false
	for _, e := range call.env {
		if e == "SERMO_WATCH=test-hook-watch" {
			hasWatch = true
		}
		if e == "SERMO_CHECK_TYPE=command" {
			hasType = true
		}
	}
	if !hasWatch || !hasType {
		t.Fatalf("custom runner via webbackend Deps did not receive expected SERMO_ env: %v", call.env)
	}
}
