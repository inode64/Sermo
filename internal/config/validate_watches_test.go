package config

import (
	"strings"
	"testing"
)

// validateRawGlobal builds a minimal-but-valid global config (Validate always
// requires defaults.policy.cooldown via validateGlobal) carrying the given raw
// sections, then returns all issues. Tests below filter to watch issues by
// substring since every issue is Scope "global".
func validateRawGlobal(t *testing.T, global map[string]any) []Issue {
	t.Helper()
	cfg := &Config{Global: Global{
		Raw:      global,
		Defaults: map[string]any{"policy": map[string]any{"cooldown": "5m"}},
	}}
	return Validate(cfg) // package function, not a method
}

// watchIssues returns only the issues whose message mentions "watches." so the
// always-present global checks (cooldown, etc.) don't mask watch validation.
func watchIssues(issues []Issue) []Issue {
	var out []Issue
	for _, i := range issues {
		if strings.Contains(i.Msg, "watches.") {
			out = append(out, i)
		}
	}
	return out
}

func TestValidateWatchesGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"disk-root": map[string]any{
				"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateFileWatchGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"app-data": map[string]any{
				"check": map[string]any{
					"type":        "file",
					"path":        "/var/lib/app",
					"recursive":   true,
					"size":        map[string]any{"op": ">", "value": 1048576},
					"permissions": map[string]any{"on": "change"},
					"owner":       map[string]any{"on": "change"},
					"existence":   map[string]any{"on": "delete"},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/file.sh"}}},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateFileWatchErrors(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"no-cond": map[string]any{
				"check": map[string]any{"type": "file", "path": "/x"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-size": map[string]any{
				"check": map[string]any{"type": "file", "path": "/x", "size": map[string]any{"op": "><", "value": "big"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-perm": map[string]any{
				"check": map[string]any{"type": "file", "path": "/x", "permissions": map[string]any{"on": "touch"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-exist": map[string]any{
				"check": map[string]any{"type": "file", "path": "/x", "existence": map[string]any{"on": "create"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"no-path": map[string]any{
				"check": map[string]any{"type": "file", "size": map[string]any{"on": "change"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
		},
	})
	want := []string{
		"watches.no-cond.check requires at least one of size, permissions, owner, existence",
		"watches.bad-size.check.size requires on: change or {op, value}",
		"watches.bad-perm.check.permissions requires on: change",
		"watches.bad-exist.check.existence requires on: delete",
		"watches.no-path.check.path is required for a file check",
	}
	for _, w := range want {
		if !hasIssue(issues, w) {
			t.Fatalf("missing issue %q in %v", w, issues)
		}
	}
}

func TestValidateProcessWatchGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
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
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateProcessWatchGoneOnly(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"liveness": map[string]any{
				"check": map[string]any{"type": "process", "name": "nginx", "gone": true},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/down.sh"}}},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("a gone-only process watch should be valid, got %v", w)
	}
}

func TestValidateProcessWatchErrors(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"no-name": map[string]any{
				"check": map[string]any{"type": "process", "cpu": map[string]any{"op": ">", "value": 1}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"no-cond": map[string]any{
				"check": map[string]any{"type": "process", "name": "x"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-for": map[string]any{
				"check": map[string]any{"type": "process", "name": "x", "for": "soon"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-cpu": map[string]any{
				"check": map[string]any{"type": "process", "name": "x", "cpu": map[string]any{"op": "=>", "value": "lots"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
		},
	})
	want := []string{
		"watches.no-name.check.name is required for a process check",
		"watches.no-cond.check requires at least one of for, cpu, memory, io",
		"watches.bad-for.check.for \"soon\" must be a valid positive duration",
		"watches.bad-cpu.check.cpu requires {op, value} with a numeric value",
	}
	for _, w := range want {
		if !hasIssue(issues, w) {
			t.Fatalf("missing issue %q in %v", w, issues)
		}
	}
}

func TestValidateDiskInodesWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"disk-inodes": map[string]any{
				"check": map[string]any{
					"type":            "disk",
					"path":            "/",
					"inodes_used_pct": map[string]any{"op": ">=", "value": 90},
					"inodes_free":     map[string]any{"op": "<", "value": 10000},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("inode predicates should be valid, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"disk-inodes": map[string]any{
				"check": map[string]any{"type": "disk", "path": "/", "inodes_used_pct": map[string]any{"op": "=>", "value": "lots"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, "watches.disk-inodes.check.inodes_used_pct has an invalid op") {
		t.Fatalf("expected invalid inode op issue, got %v", bad)
	}
}

func TestValidateZombiesWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"zombies": map[string]any{
				"check": map[string]any{"type": "zombies", "count": map[string]any{"op": ">", "value": 20}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"z": map[string]any{
				"check": map[string]any{"type": "zombies"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, "watches.z.check.count {op, value} is required for a zombies check") {
		t.Fatalf("expected missing-count issue, got %v", bad)
	}
}

func TestValidateEntropyWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"entropy": map[string]any{
				"check": map[string]any{"type": "entropy", "avail": map[string]any{"op": "<", "value": 200}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"no-avail": map[string]any{
				"check": map[string]any{"type": "entropy"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
			"bad-op": map[string]any{
				"check": map[string]any{"type": "entropy", "avail": map[string]any{"op": "=<", "value": "x"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	for _, w := range []string{
		"watches.no-avail.check.avail {op, value} is required for an entropy check",
		"watches.bad-op.check.avail has an invalid op",
		"watches.bad-op.check.avail value \"x\" must be numeric",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateConntrackWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"conntrack": map[string]any{
				"check": map[string]any{
					"type":     "conntrack",
					"used_pct": map[string]any{"op": ">=", "value": 90},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"ct": map[string]any{
				"check": map[string]any{"type": "conntrack"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, "watches.ct.check requires at least one of used_pct/free/count") {
		t.Fatalf("expected missing-predicate issue, got %v", bad)
	}
}

func TestValidateFdsWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"fds": map[string]any{
				"check": map[string]any{
					"type":     "fds",
					"used_pct": map[string]any{"op": ">=", "value": 85},
					"free":     map[string]any{"op": "<", "value": 10000},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"no-pred": map[string]any{
				"check": map[string]any{"type": "fds"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
			"bad-op": map[string]any{
				"check": map[string]any{"type": "fds", "used_pct": map[string]any{"op": "=>", "value": "lots"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	for _, w := range []string{
		"watches.no-pred.check requires at least one of used_pct/free/allocated",
		"watches.bad-op.check.used_pct has an invalid op",
		"watches.bad-op.check.used_pct value \"lots\" must be numeric",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateOomWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"oom-bare": map[string]any{ // no delta: defaults to any kill
				"check": map[string]any{"type": "oom"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
			"oom-burst": map[string]any{
				"check": map[string]any{"type": "oom", "delta": map[string]any{"op": ">", "value": 3}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/y"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"oom": map[string]any{
				"check": map[string]any{"type": "oom", "delta": map[string]any{"op": "=>", "value": "many"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	for _, w := range []string{
		"watches.oom.check.delta has an invalid op",
		"watches.oom.check.delta value \"many\" must be numeric",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateLoadWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"load": map[string]any{
				"check": map[string]any{
					"type":    "load",
					"per_cpu": true,
					"load5":   map[string]any{"op": ">", "value": 1.0},
					"load15":  map[string]any{"op": ">", "value": 0.8},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"no-pred": map[string]any{
				"check": map[string]any{"type": "load", "per_cpu": "yes"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	for _, w := range []string{
		"watches.no-pred.check.per_cpu must be a boolean",
		"watches.no-pred.check requires at least one of load1/load5/load15",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateSwapWatchGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"swap": map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"usage": map[string]any{
						"used_pct": map[string]any{"op": ">=", "value": 80},
						"free_pct": map[string]any{"op": "<", "value": 10},
						"then":     map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
					},
					"io": map[string]any{
						"delta": map[string]any{"op": ">", "value": 1000},
						"then":  map[string]any{"hook": map[string]any{"command": []any{"/y"}}},
					},
				},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateSwapWatchErrors(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"no-metrics": map[string]any{
				"check": map[string]any{"type": "swap"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
			"empty-usage": map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"usage": map[string]any{"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
				},
			},
			"bad-io": map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"io": map[string]any{"delta": map[string]any{"op": "=>", "value": "lots"}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
				},
			},
			"bad-metric": map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"bogus": map[string]any{"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
				},
			},
		},
	})
	want := []string{
		"watches.no-metrics.metrics is required and must be non-empty for a swap check",
		"watches.empty-usage.metrics.usage requires at least one of used_pct/free_pct/free_bytes",
		"watches.bad-io.metrics.io.delta has an invalid op",
		"watches.bad-metric.metrics.bogus is not a supported swap metric (usage, io)",
	}
	for _, w := range want {
		if !hasIssue(issues, w) {
			t.Fatalf("missing issue %q in %v", w, issues)
		}
	}
}

func TestValidateWatchesGoodForWindow(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"disk-root": map[string]any{
				"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
				"for":   map[string]any{"cycles": 3},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateWatchesBad(t *testing.T) {
	cases := map[string]map[string]any{
		"unknown type":     {"check": map[string]any{"type": "bogus"}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"disk no path":     {"check": map[string]any{"type": "disk", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"bad op":           {"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": "=>", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"empty cmd":        {"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{}}}},
		"for cycles 0":     {"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "for": map[string]any{"cycles": 0}},
		"within cycles -1": {"check": map[string]any{"type": "disk", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "within": map[string]any{"cycles": -1}},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			issues := watchIssues(validateRawGlobal(t, map[string]any{"watches": map[string]any{"w": w}}))
			if len(issues) == 0 {
				t.Fatalf("%s: expected a watch issue", name)
			}
		})
	}
}

func TestValidateWatchesMessageMentionsName(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{"disk-root": map[string]any{"check": map[string]any{"type": "disk"}}},
	})
	joined := ""
	for _, i := range watchIssues(issues) {
		joined += i.Msg
	}
	if !strings.Contains(joined, "disk-root") {
		t.Fatalf("issue should name the watch: %v", issues)
	}
}

func TestValidateWatchesNetGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"net-eth0": map[string]any{
				"check": map[string]any{"type": "net", "interface": "eth0"},
				"metrics": map[string]any{
					"state":  map[string]any{"on": "change", "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
					"errors": map[string]any{"delta": map[string]any{"op": ">", "value": 100}, "then": map[string]any{"hook": map[string]any{"command": []any{"/y"}}}},
				},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateWatchesNetBad(t *testing.T) {
	hook := map[string]any{"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}}
	merge := func(m map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range hook {
			out[k] = v
		}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	cases := map[string]map[string]any{
		"no interface": {"check": map[string]any{"type": "net"}, "metrics": map[string]any{"state": merge(map[string]any{"on": "change"})}},
		"no metrics":   {"check": map[string]any{"type": "net", "interface": "eth0"}},
		"unknown metric": {"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{"bogus": merge(map[string]any{"on": "change"})}},
		"bad state": {"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{"state": merge(map[string]any{})}}, // no on/expect
		"bad errors op": {"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{"errors": merge(map[string]any{"delta": map[string]any{"op": "=>", "value": 1}})}},
		"empty hook cmd": {"check": map[string]any{"type": "net", "interface": "eth0"},
			"metrics": map[string]any{"state": map[string]any{"on": "change", "then": map[string]any{"hook": map[string]any{"command": []any{}}}}}},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			issues := watchIssues(validateRawGlobal(t, map[string]any{"watches": map[string]any{"net-eth0": w}}))
			if len(issues) == 0 {
				t.Fatalf("%s: expected a watch issue", name)
			}
		})
	}
}

func TestValidateWatchesICMPGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"ping-gw": map[string]any{
				"check": map[string]any{"type": "icmp", "host": "8.8.8.8", "count": 3},
				"metrics": map[string]any{
					"state":   map[string]any{"on": "change", "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
					"latency": map[string]any{"threshold": map[string]any{"op": ">", "value": 100}, "then": map[string]any{"hook": map[string]any{"command": []any{"/y"}}}},
				},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateWatchesICMPBad(t *testing.T) {
	hook := map[string]any{"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}}
	merge := func(m map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range hook {
			out[k] = v
		}
		for k, v := range m {
			out[k] = v
		}
		return out
	}
	cases := map[string]map[string]any{
		"no host":    {"check": map[string]any{"type": "icmp"}, "metrics": map[string]any{"state": merge(map[string]any{"on": "change"})}},
		"bad count":  {"check": map[string]any{"type": "icmp", "host": "h", "count": 0}, "metrics": map[string]any{"state": merge(map[string]any{"on": "change"})}},
		"no metrics": {"check": map[string]any{"type": "icmp", "host": "h"}},
		"unknown metric": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"bogus": merge(map[string]any{"on": "change"})}},
		"bad state": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"state": merge(map[string]any{})}},
		"latency neither": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"latency": merge(map[string]any{})}},
		"bad threshold op": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"latency": merge(map[string]any{"threshold": map[string]any{"op": "=>", "value": 1}})}},
		"bad change delta": {"check": map[string]any{"type": "icmp", "host": "h"},
			"metrics": map[string]any{"latency": merge(map[string]any{"change": map[string]any{"delta": "abc"}})}},
	}
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			issues := watchIssues(validateRawGlobal(t, map[string]any{"watches": map[string]any{"ping-gw": w}}))
			if len(issues) == 0 {
				t.Fatalf("%s: expected a watch issue", name)
			}
		})
	}
}
