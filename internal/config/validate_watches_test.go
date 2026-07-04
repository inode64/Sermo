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
			"storage-root": map[string]any{
				"monitor": "previous",
				"check":   map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then":    map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateWatchesNotifyIntervalBadDuration(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"storage-root": map[string]any{
				"monitor": "previous",
				"check":   map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then": map[string]any{
					"hook":            map[string]any{"command": []any{"/usr/local/bin/alert.sh"}},
					"notify_interval": "soon",
				},
			},
		},
	})
	if !hasIssueContaining(watchIssues(issues), "notify_interval") {
		t.Fatalf("expected a notify_interval duration issue, got %v", watchIssues(issues))
	}
}

func TestValidateWatchesNotifyIntervalWithoutTargets(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"storage-root": map[string]any{
				"monitor": "previous",
				"check":   map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then": map[string]any{
					"hook":            map[string]any{"command": []any{"/usr/local/bin/alert.sh"}},
					"notify_interval": "30m",
				},
			},
		},
	})
	if !hasIssueContaining(watchIssues(issues), "no effect without notify targets") {
		t.Fatalf("expected a 'no notify targets' issue, got %v", watchIssues(issues))
	}
}

func hasIssueContaining(issues []Issue, substr string) bool {
	for _, i := range issues {
		if strings.Contains(i.Msg, substr) {
			return true
		}
	}
	return false
}

func TestValidateWatchesSingleShotParity(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"automount": map[string]any{
				"check": map[string]any{"type": "autofs"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/autofs.sh"}}},
			},
			"sqlite": map[string]any{
				"check": map[string]any{"type": "sqlite", "path": "/var/lib/app/app.db"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/sqlite.sh"}}},
			},
			"smtp": map[string]any{
				"check": map[string]any{"type": "smtp", "host": "127.0.0.1"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/smtp.sh"}}},
			},
			"ws": map[string]any{
				"check": map[string]any{"type": "websocket", "url": "ws://127.0.0.1/ws"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/ws.sh"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("single-shot watches should validate, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"metric": map[string]any{
				"check": map[string]any{"type": "metric", "name": "cpu", "op": ">", "value": "90"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
			"service": map[string]any{
				"check": map[string]any{"type": "service", "expect": "active"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	for _, want := range []string{
		`watches.metric.check.type "metric" is not supported`,
		`watches.service.check.type "service" is not supported`,
	} {
		if !hasIssue(bad, want) {
			t.Fatalf("missing issue %q in %v", want, bad)
		}
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

func TestValidateProcessWatchKillGood(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"kill-stale-sudo": map[string]any{
				"check": map[string]any{"type": "process", "name": "/usr/bin/sudo", "user": "root", "for": "120m"},
				"then":  map[string]any{"kill": map[string]any{"signal": "TERM"}},
			},
			"kill-escalate": map[string]any{
				"check": map[string]any{"type": "process", "name": "/usr/bin/sudo", "user": "root", "for": "120m"},
				"then": map[string]any{"kill": map[string]any{
					"signal":       "KILL",
					"escalate":     true,
					"term_timeout": "10s",
					"kill_timeout": "5s",
				}},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("a kill-only process watch should be valid, got %v", w)
	}
}

func TestValidateProcessWatchKillErrors(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"bad-signal": map[string]any{
				"check": map[string]any{"type": "process", "name": "sudo", "for": "1m"},
				"then":  map[string]any{"kill": map[string]any{"signal": "HUP"}},
			},
			"bad-escalate": map[string]any{
				"check": map[string]any{"type": "process", "name": "sudo", "for": "1m"},
				"then":  map[string]any{"kill": map[string]any{"escalate": "yes"}},
			},
			"bad-timeout": map[string]any{
				"check": map[string]any{"type": "process", "name": "sudo", "for": "1m"},
				"then":  map[string]any{"kill": map[string]any{"escalate": true, "term_timeout": "soon"}},
			},
			"basename-kill": map[string]any{
				"check": map[string]any{"type": "process", "name": "sudo", "user": "root", "for": "1m"},
				"then":  map[string]any{"kill": map[string]any{"signal": "TERM"}},
			},
			"missing-user-kill": map[string]any{
				"check": map[string]any{"type": "process", "name": "/usr/bin/sudo", "for": "1m"},
				"then":  map[string]any{"kill": map[string]any{"signal": "TERM"}},
			},
			// kill is process-only; on a storage watch it must be rejected.
			"kill-on-storage": map[string]any{
				"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then":  map[string]any{"kill": map[string]any{"signal": "TERM"}},
			},
		},
	})
	want := []string{
		"watches.bad-signal.then.kill.signal \"HUP\" must be TERM or KILL",
		"watches.bad-escalate.then.kill.escalate must be a boolean",
		"watches.bad-timeout.then.kill.term_timeout \"soon\" must be a valid positive duration",
		"watches.basename-kill.then.kill requires check.name to be an absolute resolved exe path",
		"watches.missing-user-kill.then.kill requires check.user",
		"watches.kill-on-storage.then.kill is only valid on a process watch",
	}
	for _, w := range want {
		if !hasIssue(issues, w) {
			t.Fatalf("missing issue %q in %v", w, watchIssues(issues))
		}
	}
}

func TestValidateStorageInodesWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"storage-inodes": map[string]any{
				"check": map[string]any{
					"type":            "storage",
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
			"storage-inodes": map[string]any{
				"check": map[string]any{"type": "storage", "path": "/", "inodes_used_pct": map[string]any{"op": "=>", "value": "lots"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, "watches.storage-inodes.check.inodes_used_pct has an invalid op") {
		t.Fatalf("expected invalid inode op issue, got %v", bad)
	}
}

func TestValidateStorageBytePredicates(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"storage-bytes": map[string]any{
				"check": map[string]any{
					"type":       "storage",
					"path":       "/",
					"free_bytes": map[string]any{"op": "<", "value": "10G"},
					"used_bytes": map[string]any{"op": ">=", "value": "100G"},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("byte predicates should be valid, got %v", w)
	}

	percent := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"storage-percent": map[string]any{
				"check": map[string]any{
					"type":     "storage",
					"path":     "/",
					"used_pct": map[string]any{"op": ">=", "value": "90%"},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(percent); len(w) != 0 {
		t.Fatalf("percent-suffixed predicate should be valid, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"storage-bytes": map[string]any{
				"check": map[string]any{"type": "storage", "path": "/", "free_bytes": map[string]any{"op": "<", "value": "lots"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, "watches.storage-bytes.check.free_bytes value \"lots\" must include a size suffix") {
		t.Fatalf("expected invalid byte-size issue, got %v", bad)
	}

	unitless := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"storage-bytes": map[string]any{
				"check": map[string]any{"type": "storage", "path": "/", "free_bytes": map[string]any{"op": "<", "value": 10}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(unitless, "watches.storage-bytes.check.free_bytes value \"10\" must include a size suffix") {
		t.Fatalf("expected missing suffix issue, got %v", unitless)
	}
}

func TestValidateNotifiers(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"notifiers": map[string]any{
			"ops-email": map[string]any{
				"type": "email",
				"dsn":  "smtp://user:pass@smtp.example.com:587",
				"from": "sermo@example.com",
				"to":   []any{"ops@example.com", "oncall@example.com"},
			},
			"team-slack": map[string]any{
				"type":    "slack",
				"webhook": "https://hooks.slack.com/services/T/B/x",
			},
			"tty-root": map[string]any{
				"type":  "tty",
				"users": []any{"root"},
			},
			"wall": map[string]any{
				"type": "wall",
			},
			"staged": map[string]any{
				"enabled": false,
			},
		},
		"notify": []any{"staged", "tty-root", "wall"},
	})
	for _, i := range good {
		if strings.Contains(i.Msg, "notifiers.") {
			t.Fatalf("valid notifier flagged: %v", good)
		}
	}

	bad := validateRawGlobal(t, map[string]any{
		"notifiers": map[string]any{
			"no-dsn":      map[string]any{"type": "email", "from": "x@y", "to": []any{"a@b"}},
			"no-to":       map[string]any{"type": "email", "dsn": "smtp://x", "from": "x@y"},
			"bad-to":      map[string]any{"type": "email", "dsn": "smtp://x", "from": "x@y", "to": []any{"a@b", 7}},
			"bad-dsn":     map[string]any{"type": "email", "dsn": "http://x", "from": "x@y", "to": []any{"a@b"}},
			"no-webhook":  map[string]any{"type": "slack"},
			"bad-webhook": map[string]any{"type": "slack", "webhook": "ftp://x"},
			"bad-type":    map[string]any{"type": "smoke-signal"},
			"no-type":     map[string]any{"dsn": "smtp://x"},
			"bad-enabled": map[string]any{"enabled": "false", "type": "slack", "webhook": "https://hooks.example/x"},
			"bad-users":   map[string]any{"type": "tty", "users": []any{"root", 7}},
			"wall-users":  map[string]any{"type": "wall", "users": []any{"root"}},
		},
	})
	for _, w := range []string{
		"notifiers.no-dsn.dsn is required for an email notifier",
		"notifiers.no-to.to must list at least one address",
		"notifiers.bad-to.to must list at least one address",
		"notifiers.bad-dsn.dsn must be an smtp:// or smtps:// URL",
		"notifiers.no-webhook.webhook is required for a slack notifier",
		"notifiers.bad-webhook.webhook must be an http(s) URL",
		"notifiers.bad-type.type \"smoke-signal\" is not supported (email, slack, teams, tty, wall)",
		"notifiers.no-type.type is required",
		"notifiers.bad-enabled.enabled must be a boolean",
		"notifiers.bad-users.users must be a string or list of strings",
		"notifiers.wall-users.users is not supported for a wall notifier; use type tty to target specific users",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateNotifyReferences(t *testing.T) {
	notifiers := map[string]any{
		"ops-email": map[string]any{"type": "email", "dsn": "smtp://x", "from": "x@y", "to": []any{"a@b"}},
	}
	storageCheck := map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}

	good := validateRawGlobal(t, map[string]any{
		"notifiers": notifiers,
		"notify":    []any{"ops-email"},
		"watches": map[string]any{
			"storage-root": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{"notify": []any{"ops-email"}}, // notify-only, no hook
			},
			"storage-expand": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{"notify": []any{"none"}, "expand": map[string]any{"by": "5G"}},
			},
			"storage-dry-run": map[string]any{
				"dry_run": true,
				"check":   storageCheck,
				"then":    map[string]any{"notify": []any{"ops-email"}},
			},
			"storage-inherit": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{},
			},
			"storage-inherit-without-then": map[string]any{
				"check": storageCheck,
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("a notify-only watch with a valid reference should pass, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"notifiers": notifiers,
		"notify":    []any{"ops-email"},
		"watches": map[string]any{
			"storage-root": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{"notify": []any{"ops-email", "ghost"}},
			},
			"no-action": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{},
			},
			"no-action-none": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{"notify": []any{"none"}},
			},
			"bad-then": map[string]any{
				"check": storageCheck,
				"then":  "notify me",
			},
			"bad-dry-run": map[string]any{
				"dry_run": "yes",
				"check":   storageCheck,
				"then":    map[string]any{"notify": []any{"none"}},
			},
		},
	})
	for _, w := range []string{
		"watches.storage-root.then.notify references unknown notifier \"ghost\"",
		"watches.bad-then.then must be a mapping",
		"watches.bad-dry-run.dry_run must be a boolean",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
	// The explicit `notify: [none]` opt-out is a deliberate monitor-only watch,
	// valid with no hook/expand and no global default.
	if hasIssue(bad, "no-action-none") {
		t.Fatalf("notify [none] must be a valid action choice: %v", bad)
	}

	noDefault := validateRawGlobal(t, map[string]any{
		"notifiers": notifiers,
		"watches": map[string]any{
			"no-action": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{},
			},
			"dry-run-only": map[string]any{
				"dry_run": true,
				"check":   storageCheck,
			},
		},
	})
	if !hasIssue(noDefault, "watches.no-action.then requires a hook, notify, kill and/or expand") {
		t.Fatalf("expected empty then without global notify to fail, got %v", noDefault)
	}
	if hasIssue(noDefault, "watches.dry-run-only") {
		t.Fatalf("dry_run top-level must be valid without actions, got %v", noDefault)
	}

	// Bare watch (no "then" key at all) with check+for is valid as alert-only:
	// produces firing events / web state but no actions (even if globals exist).
	bare := validateRawGlobal(t, map[string]any{
		"notify": []any{"ops-email"}, // globals should be ignored for bare
		"watches": map[string]any{
			"mem-high": map[string]any{
				"check": map[string]any{
					"type":     "memory",
					"used_pct": map[string]any{"op": ">=", "value": "90%"},
				},
				"for": map[string]any{"cycles": 3},
				// deliberately no "then"
			},
		},
	})
	if w := watchIssues(bare); len(w) != 0 {
		t.Fatalf("bare watch (no then) should be valid alert-only, got issues: %v", w)
	}
}

func TestValidateServiceCheckAsWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"health": map[string]any{
				"check": map[string]any{"type": "http", "url": "http://127.0.0.1/health", "expect_status": 200},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/down.sh"}}},
			},
			"port": map[string]any{
				"check": map[string]any{"type": "tcp", "port": 5432},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("service checks should be valid as watches, got %v", w)
	}

	badExpand := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"load": map[string]any{
				"check": map[string]any{"type": "load", "load1": map[string]any{"op": ">", "value": 8}},
				"then":  map[string]any{"expand": map[string]any{"by": "5G"}},
			},
		},
	})
	if !hasIssue(badExpand, "watches.load.then.expand is only valid on a storage watch") {
		t.Fatalf("non-storage expand should be rejected: %v", badExpand)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"no-url": map[string]any{
				"check": map[string]any{"type": "http"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
			"weird": map[string]any{
				"check": map[string]any{"type": "definitely-not-a-check"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	for _, w := range []string{
		"watches.no-url.check.url is required for an http check",
		"watches.weird.check.type \"definitely-not-a-check\" is not supported",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
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
	if !hasIssue(bad, "watches.z.check requires at least one of count") {
		t.Fatalf("expected missing-count issue, got %v", bad)
	}
}

func TestValidatePortsWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"scan": map[string]any{
				"check": map[string]any{"type": "ports", "host": "10.0.0.1", "ports": "22,80,443", "match": "all"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("a ports watch should be valid, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"scan": map[string]any{
				"check": map[string]any{"type": "ports", "ports": "bad"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, `watches.scan.check.ports has an invalid port`) {
		t.Fatalf("expected invalid-ports issue, got %v", bad)
	}
}

func TestValidateCertWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"api-cert": map[string]any{
				"check": map[string]any{"type": "cert", "host": "api.example.com", "expires_in_days": 14, "on_issuer_change": true},
				"then":  map[string]any{"notify": []any{"x"}},
			},
		},
		"notifiers": map[string]any{"x": map[string]any{"type": "slack", "webhook": "https://h/x"}},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("a cert watch should be valid, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"c": map[string]any{
				"check": map[string]any{"type": "cert"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, "watches.c.check requires a host or a path") {
		t.Fatalf("expected missing-host issue, got %v", bad)
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
		"watches.no-avail.check requires at least one of avail",
		"watches.bad-op.check.avail has an invalid op",
		"watches.bad-op.check.avail value \"x\" must be numeric",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateStorageMountWatch(t *testing.T) {
	// A storage watch can carry a mount condition (mount + space in one entry).
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"data-mount": map[string]any{
				"check": map[string]any{"type": "storage", "path": "/data", "mounted": true},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("a disk+mount watch should be valid, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"m": map[string]any{
				"check": map[string]any{"type": "storage", "path": "/data"}, // no predicate, no mount condition
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
			"unsupported-mount-controls": map[string]any{
				"check": map[string]any{
					"type":    "storage",
					"path":    "/data",
					"mounted": true,
					"fstype":  "ext4",
					"device":  "/dev/sdb1",
					"options": []any{"rw"},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, "watches.m.check requires a space/inode predicate") {
		t.Fatalf("expected combined-requirement issue, got %v", bad)
	}
	for _, want := range []string{
		"watches.unsupported-mount-controls.check.fstype is not supported for a storage check",
		"watches.unsupported-mount-controls.check.device is not supported for a storage check",
		"watches.unsupported-mount-controls.check.options is not supported for a storage check",
	} {
		if !hasIssue(bad, want) {
			t.Fatalf("missing issue %q in %v", want, bad)
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
		"watches.bad-op.check.used_pct value \"lots\" must be a percentage in 0..100",
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
			"storage-root": map[string]any{
				"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
				"for":   map[string]any{"cycles": 3},
			},
			"storage-duration": map[string]any{
				"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 95}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
				"for":   map[string]any{"duration": "6m"},
			},
			"storage-within-duration": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 97}},
				"then":   map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
				"within": map[string]any{"duration": "30m", "min_matches": 3},
			},
		},
	})
	if w := watchIssues(issues); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

func TestValidateWatchesBad(t *testing.T) {
	cases := map[string]map[string]any{
		"unknown type":           {"check": map[string]any{"type": "bogus"}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"storage no path":        {"check": map[string]any{"type": "storage", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"bad op":                 {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": "=>", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"empty cmd":              {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{}}}},
		"empty string cmd":       {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{""}}}},
		"non-string cmd":         {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x", 7}}}},
		"for cycles 0":           {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "for": map[string]any{"cycles": 0}},
		"for duration bad":       {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "for": map[string]any{"duration": "soon"}},
		"for both lengths":       {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "for": map[string]any{"cycles": 3, "duration": "6m"}},
		"for unexpected":         {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "for": map[string]any{"cycles": 3, "unexpected": true}},
		"within cycles -1":       {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "within": map[string]any{"cycles": -1}},
		"within duration bad":    {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "within": map[string]any{"duration": "-1m"}},
		"within min 0":           {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "within": map[string]any{"cycles": 5, "min_matches": 0}},
		"within unexpected":      {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "within": map[string]any{"cycles": 5, "min_matches": 2, "unexpected": true}},
		"both for within":        {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}, "for": map[string]any{"cycles": 3}, "within": map[string]any{"cycles": 5, "min_matches": 2}},
		"bad monitor":            {"monitor": "paused", "check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"bad display_name":       {"display_name": []any{"root"}, "check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"bad description":        {"description": []any{"root"}, "check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"bad category":           {"category": []any{"storage"}, "check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}},
		"hook bad expect_exit":   {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}, "expect_exit": "nope"}}},
		"hook bad expect_stdout": {"check": map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}}, "then": map[string]any{"hook": map[string]any{"command": []any{"/x"}, "expect_stdout": map[string]any{"op": "=>", "value": "1"}}}},
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
		"watches": map[string]any{"storage-root": map[string]any{"check": map[string]any{"type": "storage"}}},
	})
	joined := ""
	for _, i := range watchIssues(issues) {
		joined += i.Msg
	}
	if !strings.Contains(joined, "storage-root") {
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

func TestValidateWatchPolicy(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"grow": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/data", "free_pct": map[string]any{"op": "<", "value": 10}},
				"policy": map[string]any{"cooldown": "30m"},
				"then":   map[string]any{"expand": map[string]any{"by": "5G"}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("valid watch policy flagged: %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"bad-cooldown": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/data", "free_pct": map[string]any{"op": "<", "value": 10}},
				"policy": map[string]any{"cooldown": "-5m"},
				"then":   map[string]any{"expand": map[string]any{"by": "5G"}},
			},
			"bad-shape": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/data", "free_pct": map[string]any{"op": "<", "value": 10}},
				"policy": "30m",
				"then":   map[string]any{"expand": map[string]any{"by": "5G"}},
			},
			"bad-actions": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/data", "free_pct": map[string]any{"op": "<", "value": 10}},
				"policy": map[string]any{"cooldown": "30m", "max_actions": 3},
				"then":   map[string]any{"expand": map[string]any{"by": "5G"}},
			},
		},
	})
	for _, w := range []string{
		`watches.bad-cooldown.policy.cooldown "-5m" must be a valid positive duration`,
		"watches.bad-shape.policy must be a mapping",
		"watches.bad-actions.policy.max_actions requires policy.max_actions_window",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateExpandBy(t *testing.T) {
	storageCheck := map[string]any{"type": "storage", "path": "/data", "free_pct": map[string]any{"op": "<", "value": 10}}
	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"no-by": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{"expand": map[string]any{}},
			},
			"unitless": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{"expand": map[string]any{"by": 1024}},
			},
			"bad-shape": map[string]any{
				"check": storageCheck,
				"then":  map[string]any{"expand": "5G"},
			},
		},
	})
	for _, w := range []string{
		`watches.no-by.then.expand.by "" must be a positive size with a K/M/G/T suffix`,
		`watches.unitless.then.expand.by "1024" must be a positive size with a K/M/G/T suffix`,
		"watches.bad-shape.then.expand must be a mapping with a `by` size",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateSwapUsageSharedGrammar(t *testing.T) {
	// Percent and byte-size forms work in swap usage exactly like in storage
	// (section: unified checks — one predicate grammar for every level check).
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"swap": map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"usage": map[string]any{
						"used_pct":   map[string]any{"op": ">=", "value": "85%"},
						"free_bytes": map[string]any{"op": "<", "value": "1G"},
						"then":       map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
					},
				},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("percent/size forms should be valid in swap usage, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"swap": map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"usage": map[string]any{
						"used_pct":   map[string]any{"op": ">=", "value": "150%"},
						"free_bytes": map[string]any{"op": "<", "value": 1024},
						"then":       map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
					},
				},
			},
		},
	})
	for _, w := range []string{
		`used_pct value "150%" must be a percentage in 0..100`,
		`free_bytes value "1024" must include a size suffix`,
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateMetricWatchEntryLevelBlocks(t *testing.T) {
	// then/for/within on a multi-metric watch entry belong in each metric's own
	// block, so validation must reject entry-level copies.
	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"swap": map[string]any{
				"check": map[string]any{"type": "swap"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
				"for":   map[string]any{"cycles": 3},
				"metrics": map[string]any{
					"io": map[string]any{
						"delta": map[string]any{"op": ">", "value": 1000},
						"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
					},
				},
			},
			"net": map[string]any{
				"check": map[string]any{"type": "net", "interface": "eth0"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
				"metrics": map[string]any{
					"state": map[string]any{
						"on":   "change",
						"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
					},
				},
			},
		},
	})
	for _, w := range []string{
		"watches.swap.then is not valid on a multi-metric watch",
		"watches.swap.for is not valid on a multi-metric watch",
		"watches.net.then is not valid on a multi-metric watch",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}

	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"swap": map[string]any{
				"check": map[string]any{"type": "swap"},
				"metrics": map[string]any{
					"io": map[string]any{
						"delta": map[string]any{"op": ">", "value": 1000},
						"for":   map[string]any{"cycles": 3},
						"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
					},
				},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("per-metric then/for should be valid, got %v", w)
	}
}

func TestValidateWithinMinMatchesOptional(t *testing.T) {
	// min_matches defaults to 1; only an explicit invalid value is an error.
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"root": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"within": map[string]any{"cycles": 5},
				"then":   map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("within without min_matches should be valid, got %v", w)
	}
}

func TestValidateWatchWithoutCheckStillValidatesEntry(t *testing.T) {
	// A missing check must not mask the entry-level problems: everything is
	// reported in one validation pass.
	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"broken": map[string]any{
				"interval": "soon",
				"within":   map[string]any{"cycles": 0},
				"policy":   map[string]any{"cooldown": "-1m"},
				"then":     map[string]any{"notify": []any{"ghost"}},
			},
		},
	})
	for _, w := range []string{
		"watches.broken.check is required",
		`watches.broken.interval "soon" must be a valid positive duration`,
		"watches.broken.within.cycles must be > 0",
		`watches.broken.policy.cooldown "-1m" must be a valid positive duration`,
		`watches.broken.then.notify references unknown notifier "ghost"`,
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateScalarWithinRejectedOnWatch(t *testing.T) {
	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"root": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"within": "1h",
				"then":   map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, "watches.root.within must be a mapping") {
		t.Fatalf("scalar within should be rejected, got %v", bad)
	}
}

func TestValidateMemoryWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"ram": map[string]any{
				"check": map[string]any{
					"type":            "memory",
					"used_pct":        map[string]any{"op": ">=", "value": "90%"},
					"available_bytes": map[string]any{"op": "<", "value": "1G"},
				},
				"for":  map[string]any{"cycles": 3},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("valid memory watch flagged: %v", w)
	}
}

func TestValidatePressureWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"mem-stall": map[string]any{
				"check": map[string]any{
					"type":       "pressure",
					"resource":   "memory",
					"some_avg10": map[string]any{"op": ">", "value": 10},
				},
				"for":  map[string]any{"cycles": 3},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("valid pressure watch flagged: %v", w)
	}
}

func TestValidatePidsWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"pid-table": map[string]any{
				"check": map[string]any{"type": "pids", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"for":   map[string]any{"cycles": 3},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("valid pids watch flagged: %v", w)
	}
}

func TestValidateDiskIOWatch(t *testing.T) {
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"sda-busy": map[string]any{
				"check": map[string]any{
					"type":     "diskio",
					"device":   "sda",
					"util_pct": map[string]any{"op": ">=", "value": 90},
					"await_ms": map[string]any{"op": ">", "value": 50},
				},
				"for":  map[string]any{"cycles": 3},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("valid diskio watch flagged: %v", w)
	}
}

func TestValidateWatchPortRangeMatchesServices(t *testing.T) {
	// A tcp/connection check used as a watch enforces the same 1..65535 port
	// range walkScalars applies to resolved services.
	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"tcp-high": map[string]any{
				"check": map[string]any{"type": "tcp", "port": 99999},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
			"conn-high": map[string]any{
				"check": map[string]any{"type": "smtp", "host": "127.0.0.1", "port": 99999},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	for _, w := range []string{
		"watches.tcp-high.check.port is required and must be a port in 1..65535",
		`watches.conn-high.check.port "99999" must be an integer in 1..65535`,
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}

func TestValidateFileProcessWatchRejectsEntryLevelWindow(t *testing.T) {
	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"cfg": map[string]any{
				"check": map[string]any{
					"type": "file", "path": "/etc/app.conf",
					"size": map[string]any{"on": "change"},
				},
				"for":  map[string]any{"cycles": 3},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
			"proc": map[string]any{
				"check":  map[string]any{"type": "process", "name": "nginx", "cpu": map[string]any{"op": ">", "value": 90}},
				"within": map[string]any{"cycles": 5, "min_matches": 2},
				"then":   map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	for _, w := range []string{
		"watches.cfg.for is not valid on a file watch",
		"watches.proc.within is not valid on a process watch",
	} {
		if !hasIssue(bad, w) {
			t.Fatalf("missing issue %q in %v", w, bad)
		}
	}
}
