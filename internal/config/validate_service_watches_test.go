package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServiceWatchesSurviveResolution proves a service's `watches:` section is
// preserved through catalog merge + variable expansion, so the daemon's worker
// builder actually sees it (the runtime wiring depends on resolved.Tree carrying
// the section).
func TestServiceWatchesSurviveResolution(t *testing.T) {
	root := repoRoot(t)
	dir := t.TempDir()
	global := filepath.Join(dir, "sermo.yml")
	body := "paths:\n  services: [" + filepath.Join(root, "examples", "services") + "]\n  runtime: /run/sermo\n" +
		"defaults:\n  policy: { cooldown: 5m }\n"
	if err := os.WriteFile(global, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadConfig(t, global, WithCatalogDirs(filepath.Join(root, "catalog")))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	resolved, errs := cfg.Resolve("mail-queue")
	if len(errs) != 0 {
		t.Fatalf("Resolve(mail-queue): %v", errs)
	}
	watches, ok := resolved.Tree["watches"].(map[string]any)
	if !ok || len(watches) != 3 {
		t.Fatalf("resolved tree watches = %v, want the 3 example service watches", resolved.Tree["watches"])
	}
	if _, ok := watches["deferred-backlog"]; !ok {
		t.Errorf("resolved watches missing deferred-backlog: %v", watches)
	}
}

func TestValidateServiceWatches(t *testing.T) {
	defined := map[string]struct{}{"ops": {}}
	run := func(watches map[string]any) []string {
		return collect(func(add func(string, ...any)) {
			validateServiceWatches(map[string]any{"watches": watches}, "/run/sermo/locks", defined, nil, add)
		})
	}

	// A valid file-count watch with a notify action passes.
	if got := run(map[string]any{
		"queue-backlog": map[string]any{
			"check": map[string]any{"type": "count", "path": "/var/spool/x", "of": "file", "count": map[string]any{"op": ">=", "value": 100}},
			"then":  map[string]any{"notify": []any{"ops"}},
		},
	}); len(got) != 0 {
		t.Errorf("valid count watch should have no issues, got: %v", got)
	}

	// A watch without then is a check-only entry that resolves to checks.<name>.
	if got := run(map[string]any{
		"service": map[string]any{
			"check": map[string]any{"type": "service", "expect": "active"},
		},
	}); len(got) != 0 {
		t.Errorf("check-only service watch should have no issues, got: %v", got)
	}

	// A check-only entry uses the normal service checks grammar, so process checks
	// remain valid even though the process watch runtime is host-wide and rejected
	// below when a then block is present.
	if got := run(map[string]any{
		"backup": map[string]any{
			"check": map[string]any{"type": "process", "exe": "/usr/bin/backup", "state": "running", "optional": true},
		},
	}); len(got) != 0 {
		t.Errorf("check-only process check should have no issues, got: %v", got)
	}

	// A process_count watch is service-scoped here, unlike a host process watch.
	if got := run(map[string]any{
		"workers": map[string]any{
			"check": map[string]any{"type": "process_count", "user": "mail", "count": map[string]any{"op": ">", "value": 3}},
			"then":  map[string]any{"notify": []any{"ops"}},
		},
	}); len(got) != 0 {
		t.Errorf("process_count watch should be valid in a service, got: %v", got)
	}

	// metric is valid in a service watch (reads a dedicated per-watch collector).
	if got := run(map[string]any{
		"cpu": map[string]any{"check": map[string]any{"type": "metric", "scope": "service", "name": "cpu_thread", "op": ">", "value": "90%"}, "then": map[string]any{"notify": []any{"ops"}}},
	}); len(got) != 0 {
		t.Errorf("metric watch should be valid in a service, got: %v", got)
	}

	// net/icmp/swap belong in the global watches: section.
	if got := run(map[string]any{
		"link": map[string]any{"check": map[string]any{"type": "net", "interface": "eth0"}, "then": map[string]any{"notify": []any{"ops"}}},
	}); !strings.Contains(strings.Join(got, "\n"), "is host-scoped") {
		t.Errorf("expected host-scoped issue for net, got: %v", got)
	}

	// The process watch (host-wide + kill) is rejected inside a service.
	if got := run(map[string]any{
		"proc": map[string]any{"check": map[string]any{"type": "process", "name": "worker"}, "then": map[string]any{"notify": []any{"ops"}}},
	}); !strings.Contains(strings.Join(got, "\n"), "matches host-wide") {
		t.Errorf("expected host-wide-process issue, got: %v", got)
	}

	// Unknown notifier reference is reported.
	if got := run(map[string]any{
		"w": map[string]any{"check": map[string]any{"type": "count", "path": "/x", "count": map[string]any{"op": ">", "value": 1}}, "then": map[string]any{"notify": []any{"ghost"}}},
	}); !strings.Contains(strings.Join(got, "\n"), "ghost") {
		t.Errorf("expected unknown-notifier issue, got: %v", got)
	}

	// Reserved names (version/config) collide with the synthesized monitors.
	for _, name := range []string{"version", "config"} {
		if got := run(map[string]any{
			name: map[string]any{"check": map[string]any{"type": "count", "path": "/x", "count": map[string]any{"op": ">", "value": 1}}, "then": map[string]any{"notify": []any{"ops"}}},
		}); !strings.Contains(strings.Join(got, "\n"), "reserved") {
			t.Errorf("watch name %q should be reserved, got: %v", name, got)
		}
	}

	// Missing check is reported.
	if got := run(map[string]any{"w": map[string]any{"then": map[string]any{"notify": []any{"ops"}}}}); !strings.Contains(strings.Join(got, "\n"), "check is required") {
		t.Errorf("expected missing-check issue, got: %v", got)
	}

	// Unknown check type is reported.
	if got := run(map[string]any{
		"w": map[string]any{"check": map[string]any{"type": "bogus"}, "then": map[string]any{"notify": []any{"ops"}}},
	}); !strings.Contains(strings.Join(got, "\n"), "is not supported") {
		t.Errorf("expected unsupported-type issue, got: %v", got)
	}

	// No watches section -> no issues.
	if got := collect(func(add func(string, ...any)) {
		validateServiceWatches(map[string]any{}, "/run/sermo/locks", defined, nil, add)
	}); len(got) != 0 {
		t.Errorf("absent watches section should have no issues, got: %v", got)
	}
}
