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

func TestBuildWatchesBuildsFile(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"app-data": map[string]any{
			"check": map[string]any{
				"type":        "file",
				"path":        "/var/lib/app",
				"recursive":   true,
				"size":        map[string]any{"op": ">", "value": 1024},
				"permissions": map[string]any{"on": "change"},
				"existence":   map[string]any{"on": "delete"},
			},
			"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/file.sh"}}},
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
	if w.Name != "app-data" || w.CheckType != "file" || w.Interval != 30*time.Second {
		t.Fatalf("unexpected watch: %+v", w)
	}
	if w.Cycle == nil {
		t.Fatal("file watch must wire a Cycle override")
	}
}

func TestBuildWatchesFileWarnsOnNoCondition(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"bad": map[string]any{
			"check": map[string]any{"type": "file", "path": "/x"},
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 || len(warns) == 0 {
		t.Fatalf("expected a warning and no watch, got %d watches, warns %v", len(watches), warns)
	}
}

func TestBuildWatchesBuildsProcess(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"hot-workers": map[string]any{
			"check": map[string]any{
				"type":   "process",
				"name":   "myworker",
				"user":   "www-data",
				"for":    "5m",
				"cpu":    map[string]any{"op": ">", "value": 80},
				"memory": map[string]any{"op": ">", "value": 524288000},
				"io":     map[string]any{"op": ">", "value": 10485760},
			},
			"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/proc.sh"}}},
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
	if w.Name != "hot-workers" || w.CheckType != "process" || w.Cycle == nil {
		t.Fatalf("unexpected watch: %+v", w)
	}
}

func TestBuildWatchesProcessWarnsOnNoCondition(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"bad": map[string]any{
			"check": map[string]any{"type": "process", "name": "x"},
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 || len(warns) == 0 {
		t.Fatalf("expected a warning and no watch, got %d watches, warns %v", len(watches), warns)
	}
}

func TestBuildWatchesExpandsSwap(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"swap": map[string]any{
			"check": map[string]any{"type": "swap"},
			"metrics": map[string]any{
				"usage": map[string]any{
					"used_pct": map[string]any{"op": ">=", "value": 80},
					"then":     map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/swap-usage.sh"}}},
				},
				"io": map[string]any{
					"delta": map[string]any{"op": ">", "value": 1000},
					"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/swap-io.sh"}}},
				},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 2 {
		t.Fatalf("expected 2 expanded watches (usage, io), got %d", len(watches))
	}
	cmds := map[string]bool{}
	for _, w := range watches {
		if w.CheckType != "swap" || w.Name != "swap" {
			t.Fatalf("unexpected watch: %+v", w)
		}
		cmds[w.Hook.Command[0]] = true
	}
	if !cmds["/usr/local/bin/swap-usage.sh"] || !cmds["/usr/local/bin/swap-io.sh"] {
		t.Fatalf("each metric should keep its own hook, got %v", cmds)
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

func TestBuildWatchesExpandsNet(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"net-eth0": map[string]any{
			"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{
				"state": map[string]any{
					"on":   "change",
					"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/state.sh"}}},
				},
				"errors": map[string]any{
					"delta": map[string]any{"op": ">", "value": 100},
					"then":  map[string]any{"hook": map[string]any{"command": []any{"/bin/err.sh"}}},
				},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 2 {
		t.Fatalf("expected 2 expanded watches, got %d", len(watches))
	}
	cmds := map[string]bool{}
	for _, w := range watches {
		if w.CheckType != "net" || w.Name != "net-eth0" || w.Interval != 30*time.Second {
			t.Fatalf("unexpected watch: %+v", w)
		}
		cmds[w.Hook.Command[0]] = true
	}
	if !cmds["/bin/state.sh"] || !cmds["/bin/err.sh"] {
		t.Fatalf("expected distinct per-metric hooks, got %v", cmds)
	}
}

func TestBuildWatchesExpandsICMP(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"ping-gw": map[string]any{
			"check": map[string]any{"type": "icmp", "host": "8.8.8.8", "count": 3},
			"metrics": map[string]any{
				"state": map[string]any{
					"on":   "change",
					"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/state.sh"}}},
				},
				"latency": map[string]any{
					"threshold": map[string]any{"op": ">", "value": 100},
					"then":      map[string]any{"hook": map[string]any{"command": []any{"/bin/lat.sh"}}},
				},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{DefaultTimeout: time.Second}, 30*time.Second)
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	if len(watches) != 2 {
		t.Fatalf("expected 2 expanded watches, got %d", len(watches))
	}
	cmds := map[string]bool{}
	for _, w := range watches {
		if w.CheckType != "icmp" || w.Name != "ping-gw" {
			t.Fatalf("unexpected watch: %+v", w)
		}
		cmds[w.Hook.Command[0]] = true
	}
	if !cmds["/bin/state.sh"] || !cmds["/bin/lat.sh"] {
		t.Fatalf("expected distinct per-metric hooks, got %v", cmds)
	}
}

func TestBuildWatchesNetWarnsOnBadMetric(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"net-eth0": map[string]any{
			"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{
				"state": map[string]any{ // missing on/expect -> check build error
					"then": map[string]any{"hook": map[string]any{"command": []any{"/bin/x.sh"}}},
				},
			},
		},
	})
	watches, warns := BuildWatches(cfg, Deps{}, time.Second)
	if len(watches) != 0 || len(warns) == 0 {
		t.Fatalf("expected 0 watches and a warning, got %d / %v", len(watches), warns)
	}
}
