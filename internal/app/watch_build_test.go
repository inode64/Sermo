package app

import (
	"testing"
	"time"

	"sermo/internal/config"
)

func cfgWithWatches(raw map[string]any) *config.Config {
	return &config.Config{Global: config.Global{Raw: map[string]any{"watches": raw}}}
}

func TestBuildWatchesBuildsDisk(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"disk-root": map[string]any{
			"check": map[string]any{
				"type":     "disk",
				"path":     "/",
				"used_pct": map[string]any{"op": ">=", "value": 90},
			},
			"for":  map[string]any{"cycles": 3},
			"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 1 {
		t.Fatalf("expected 1 watch, got %d", len(watches))
	}
	w := watches[0]
	if w.Name != "disk-root" || w.Interval != 30*time.Second {
		t.Fatalf("unexpected watch: %+v", w)
	}
	if w.Window.For == nil || w.Window.For.Cycles != 3 {
		t.Fatalf("for window not parsed: %+v", w.Window)
	}
	if len(w.Hook.Command) != 1 {
		t.Fatalf("hook command not parsed: %+v", w.Hook)
	}
}

func TestBuildWatchesSkipsDisabled(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"off": map[string]any{"enabled": false, "check": map[string]any{"type": "disk", "path": "/"}},
	})
	watches, _ := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 {
		t.Fatalf("expected disabled watch skipped, got %d", len(watches))
	}
}

func TestBuildWatchesWarnsOnBadCheck(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"bad": map[string]any{
			"check": map[string]any{"type": "disk"}, // missing path/predicate
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 || len(warns) == 0 {
		t.Fatalf("expected 0 watches and a warning, got %d / %v", len(watches), warns)
	}
}
