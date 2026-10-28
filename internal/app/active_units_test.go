package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/servicemgr"
)

// listingRunner answers the systemd active-unit listing with active and counts
// it; every other init query is procInfoRunner's.
type listingRunner struct {
	procInfoRunner
	active   []string
	mu       sync.Mutex
	listings int
}

func (r *listingRunner) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	if name == "systemctl" && len(args) > 0 && args[0] == "list-units" {
		r.mu.Lock()
		r.listings++
		r.mu.Unlock()
		var out strings.Builder
		for _, unit := range r.active {
			out.WriteString(unit + " loaded active running " + unit + "\n")
		}
		return execx.Result{Stdout: out.String()}, nil
	}
	return r.procInfoRunner.Run(ctx, name, args...)
}

// unitStatusCounter counts live status queries per unit.
type unitStatusCounter struct {
	fakeManager
	mu    sync.Mutex
	calls map[string]int
}

func (m *unitStatusCounter) Status(ctx context.Context, unit string) (servicemgr.ServiceStatus, error) {
	m.mu.Lock()
	m.calls[unit]++
	m.mu.Unlock()
	return m.fakeManager.Status(ctx, unit)
}

func (m *unitStatusCounter) count(unit string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls[unit]
}

// The cycle's service check reads the shared active-unit listing instead of
// one live query per service; a service faster than the engine interval and
// the worker's observe-only gate keep querying live.
func TestCycleServiceCheckReadsSharedActiveUnits(t *testing.T) {
	root := t.TempDir()
	enabled := filepath.Join(root, "services")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(globalPath, []byte("paths:\n  services: ["+enabled+"]\ndefaults:\n  policy:\n    cooldown: 5m\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"slow.yml": "name: slow\nservice: slow\nchecks:\n  state:\n    type: service\n    expect: active\n",
		"fast.yml": "name: fast\nservice: fast\ninterval: 10s\nchecks:\n  state:\n    type: service\n    expect: active\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(enabled, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cfg := loadTestConfig(t, globalPath)

	runner := &listingRunner{active: []string{"slow.service", "fast.service"}}
	deps, collector := parallelStartupDeps(t, runner)
	manager := &unitStatusCounter{calls: map[string]int{}}
	deps.Manager = manager
	deps.Interval = 30 * time.Second
	deps.ActiveUnits = NewActiveUnitSnapshot(deps)

	workers, _, warnings := BuildWorkers(t.Context(), cfg, deps, collector)
	if len(warnings) != 0 {
		t.Fatalf("BuildWorkers warnings = %v, want none", warnings)
	}
	byName := map[string]*Worker{}
	for _, w := range workers {
		byName[w.Service] = w
	}
	for _, name := range []string{"slow", "fast"} {
		w := byName[name]
		before := manager.count(w.Unit)
		cache := w.Checks(t.Context(), w.CheckDeps)
		if r := cache["state"]; !r.OK {
			t.Fatalf("%s state = %+v, want ok", name, r)
		}
		live := manager.count(w.Unit) - before
		if want := map[string]int{"slow": 0, "fast": 1}[name]; live != want {
			t.Fatalf("%s live status queries = %d, want %d", name, live, want)
		}
	}
	if runner.listings != 1 {
		t.Fatalf("active-unit listings = %d, want 1", runner.listings)
	}

	slow := byName["slow"]
	before := manager.count(slow.Unit)
	if !slow.backendActive(t.Context()) || manager.count(slow.Unit) != before+1 {
		t.Fatal("observe-only gate must query the backend live")
	}
}

// Only an active verdict comes from the listing: a unit it does not name is
// queried live, and no listing at all leaves the live query alone.
func TestActiveUnitStatusConfirmsOtherVerdictsLive(t *testing.T) {
	runner := &listingRunner{active: []string{"web.service"}}
	snapshot := servicemgr.NewActiveUnitSnapshot(servicemgr.BackendSystemd, runner, time.Minute, time.Second)
	liveCalls := 0
	live := func(context.Context) (servicemgr.Status, error) {
		liveCalls++
		return servicemgr.StatusFailed, nil
	}

	if st, err := activeUnitStatus(snapshot, "web.service", live)(t.Context()); err != nil || st != servicemgr.StatusActive || liveCalls != 0 {
		t.Fatalf("listed unit = %s, %v after %d live calls; want active from the listing", st, err, liveCalls)
	}
	if st, err := activeUnitStatus(snapshot, "db.service", live)(t.Context()); err != nil || st != servicemgr.StatusFailed || liveCalls != 1 {
		t.Fatalf("unlisted unit = %s, %v after %d live calls; want the live verdict", st, err, liveCalls)
	}
	if st, _ := activeUnitStatus(nil, "web.service", live)(t.Context()); st != servicemgr.StatusFailed || liveCalls != 2 {
		t.Fatalf("no listing = %s after %d live calls; want the live verdict", st, liveCalls)
	}
	if NewActiveUnitSnapshot(Deps{Backend: servicemgr.BackendDocker, Interval: time.Minute}) != nil {
		t.Fatal("a backend without unit listings must not get a snapshot")
	}
}
