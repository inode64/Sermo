package diag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
)

// fakeHost answers from in-memory sets.
type fakeHost struct {
	paths  map[string]bool
	ifaces map[string]bool
	mounts map[string]bool
}

func (h fakeHost) PathExists(p string) bool      { return h.paths[p] }
func (h fakeHost) InterfaceExists(n string) bool { return h.ifaces[n] }
func (h fakeHost) IsMountPoint(p string) bool    { return h.mounts[p] }

type fakeStore struct {
	integrity error
	tracked   []string
}

func (s fakeStore) IntegrityCheck() error                   { return s.integrity }
func (s fakeStore) TrackedControlStates() ([]string, error) { return s.tracked, nil }

// loadCfg writes a base global + one service doc under a temp dir (substituting
// @ROOT@) and loads it.
func loadCfg(t *testing.T, global, svc string) *config.Config {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "enabled"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(rel, body string) {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(strings.ReplaceAll(body, "@ROOT@", root)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("sermo.yml", global)
	if svc != "" {
		write("enabled/svc.yml", svc)
	}
	cfg, err := config.Load(filepath.Join(root, "sermo.yml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func has(r Result, level Level, substr string) bool {
	for _, f := range r.Findings {
		if f.Level == level && strings.Contains(f.Message, substr) {
			return true
		}
	}
	return false
}

const baseGlobal = `
engine: { backend: auto, interval: 30s }
paths: { services: [ @ROOT@/enabled ], state: @ROOT@/state }
defaults: { policy: { cooldown: 5m } }
`

func TestDiagnoseOrphanedData(t *testing.T) {
	cfg := loadCfg(t, baseGlobal, `
kind: service
name: web
service: nginx
policy: { cooldown: 5m }
checks:
  ping: { type: tcp, host: 127.0.0.1, port: 80 }
`)
	store := fakeStore{tracked: []string{"web", "ghost"}}
	r := Diagnose(cfg, store, fakeHost{})
	if !has(r, LevelWarning, `target "ghost"`) {
		t.Fatalf("expected orphaned-data warning for ghost: %+v", r.Findings)
	}
	if has(r, LevelWarning, `target "web"`) {
		t.Fatal("configured service web must not be flagged as orphaned")
	}
}

func TestDiagnoseDatabaseIntegrity(t *testing.T) {
	cfg := loadCfg(t, baseGlobal, "")
	r := Diagnose(cfg, fakeStore{integrity: errString("malformed")}, fakeHost{})
	if !has(r, LevelError, "database is unhealthy") {
		t.Fatalf("expected DB integrity error: %+v", r.Findings)
	}
}

func TestDiagnoseIntervalAlignment(t *testing.T) {
	cfg := loadCfg(t, baseGlobal, `
kind: service
name: web
service: nginx
policy: { cooldown: 5m }
checks:
  ok:    { type: tcp, host: 127.0.0.1, port: 80, interval: 5m }
  odd:   { type: tcp, host: 127.0.0.1, port: 80, interval: 40s }
  small: { type: tcp, host: 127.0.0.1, port: 80, interval: 10s }
`)
	r := Diagnose(cfg, nil, fakeHost{})
	if has(r, LevelWarning, "check ok") {
		t.Fatal("5m is a multiple of 30s; should not warn")
	}
	if !has(r, LevelWarning, "not a multiple") {
		t.Fatalf("40s should warn as non-multiple: %+v", r.Findings)
	}
	if !has(r, LevelWarning, "below the 30s resolution") {
		t.Fatalf("10s should warn as below resolution: %+v", r.Findings)
	}
}

func TestDiagnoseHostResources(t *testing.T) {
	global := baseGlobal + `
watches:
  link:
    check: { type: net, interface: eth0 }
    then: { hook: { command: [/bin/true] } }
  data:
    check: { type: storage, path: /data, used_pct: { op: ">", value: 90 }, mounted: true }
    then: { hook: { command: [/bin/true] } }
`
	cfg := loadCfg(t, global, "")
	// eth0 absent, /data exists but is not a mount point
	r := Diagnose(cfg, nil, fakeHost{paths: map[string]bool{"/data": true}})
	if !has(r, LevelWarning, `interface "eth0" does not exist`) {
		t.Fatalf("expected missing-interface warning: %+v", r.Findings)
	}
	if !has(r, LevelWarning, "not currently a mount point") {
		t.Fatalf("expected not-a-mount warning: %+v", r.Findings)
	}

	// with eth0 present and /data mounted -> no host warnings
	clean := Diagnose(cfg, nil, fakeHost{
		paths:  map[string]bool{"/data": true},
		ifaces: map[string]bool{"eth0": true},
		mounts: map[string]bool{"/data": true},
	})
	if clean.Warnings() != 0 {
		t.Fatalf("expected no host warnings when present: %+v", clean.Findings)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestDiagnoseNewCheckResources(t *testing.T) {
	global := baseGlobal + `
watches:
  sda-busy:
    check: { type: diskio, device: sdz, util_pct: { op: ">=", value: 90 } }
    then: { hook: { command: [/bin/true] } }
  disabled-mem-stall:
    monitor: disabled
    check: { type: pressure, resource: memory, some_avg60: { op: ">", value: 10 } }
  mem-stall:
    check: { type: pressure, resource: memory, some_avg60: { op: ">", value: 10 } }
    then: { hook: { command: [/bin/true] } }
  slow-disk:
    check: { type: hdparm, device: /dev/sdz, read: { op: "<", value: 100 } }
    then: { hook: { command: [/bin/true] } }
  dying-disk:
    check: { type: smart, device: /dev/sdz }
    then: { hook: { command: [/bin/true] } }
`
	cfg := loadCfg(t, global, "")

	// Nothing exists on this fake host.
	r := Diagnose(cfg, nil, fakeHost{})
	for _, want := range []string{
		`block device "sdz" does not exist`,
		"no /proc/pressure/memory",
		`device "/dev/sdz" does not exist`,
	} {
		if !has(r, LevelWarning, want) {
			t.Fatalf("missing warning %q in %+v", want, r.Findings)
		}
	}
	if has(r, LevelWarning, "watch disabled-mem-stall") {
		t.Fatalf("disabled watch should not be diagnosed: %+v", r.Findings)
	}

	// With every resource present there are no warnings.
	clean := Diagnose(cfg, nil, fakeHost{paths: map[string]bool{
		"/sys/class/block/sdz":  true,
		"/proc/pressure/memory": true,
		"/dev/sdz":              true,
	}})
	if clean.Warnings() != 0 {
		t.Fatalf("expected no warnings when resources exist: %+v", clean.Findings)
	}
}

func TestDiagnoseServiceCheckDeviceResources(t *testing.T) {
	// The same probes apply to a service's checks: section (unified types).
	svc := `
kind: service
name: db
service: db
checks:
  io: { type: diskio, device: sdz, util_pct: { op: ">=", value: 90 } }
`
	cfg := loadCfg(t, baseGlobal, svc)
	r := Diagnose(cfg, nil, fakeHost{})
	if !has(r, LevelWarning, `block device "sdz" does not exist`) {
		t.Fatalf("expected service-check diskio warning: %+v", r.Findings)
	}
}
