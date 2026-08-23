package config

import (
	"maps"
	"strings"
	"testing"

	"sermo/internal/checks"
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

// watchConfigs wraps several named checks in the raw-global shape, giving each a
// hook-only then block ({"hook": {"command": ["/x"]}}).
func watchConfigs(checks map[string]any) map[string]any {
	watches := make(map[string]any, len(checks))
	for name, check := range checks {
		watches[name] = map[string]any{
			"check": check,
			"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
		}
	}
	return map[string]any{"watches": watches}
}

// watchConfig wraps a single named watch's check in the raw-global shape, using
// the common hook-only then block ({"hook": {"command": ["/x"]}}).
func watchConfig(name string, check map[string]any) map[string]any {
	return watchConfigs(map[string]any{name: check})
}

// assertNoWatchIssues asserts the given raw global produces no watch issues.
func assertNoWatchIssues(t *testing.T, global map[string]any) {
	t.Helper()
	if w := watchIssues(validateRawGlobal(t, global)); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}
}

// assertWatchIssues asserts each want appears in some watch issue.
func assertWatchIssues(t *testing.T, global map[string]any, want ...string) {
	t.Helper()
	issues := watchIssues(validateRawGlobal(t, global))
	for _, w := range want {
		if !hasIssueContaining(issues, w) {
			t.Fatalf("expected a watch issue containing %q, got %v", w, issues)
		}
	}
}

// assertEachWatchInvalid runs each named case as a subtest, asserting the single
// watch (keyed by watchName) produces at least one watch issue.
func assertEachWatchInvalid(t *testing.T, watchName string, cases map[string]map[string]any) {
	t.Helper()
	for name, w := range cases {
		t.Run(name, func(t *testing.T) {
			if issues := watchIssues(validateRawGlobal(t, map[string]any{"watches": map[string]any{watchName: w}})); len(issues) == 0 {
				t.Fatalf("%s: expected a watch issue", name)
			}
		})
	}
}

func TestValidateWatchesGood(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"storage-root": map[string]any{
				"monitor": "previous",
				"check":   map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then":    map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/alert.sh"}}},
			},
		},
	})
}

func TestValidateProcessPolicyWatch(t *testing.T) {
	valid := map[string]any{
		"notifiers": map[string]any{"ops": map[string]any{"type": "wall"}},
		"watches": map[string]any{
			"postgres-policy": map[string]any{
				"check": map[string]any{
					"type": checks.CheckTypeProcessPolicy,
					"user": "postgres",
					"allow": map[string]any{
						"postmaster": map[string]any{
							"exe": "/usr/lib64/postgresql-18/bin/postgres",
							"cmd": "^postgres -D /srv/postgres$",
						},
					},
				},
				"then": map[string]any{
					"notify":          []any{"ops"},
					"notify_interval": "5m",
				},
			},
		},
	}
	assertNoWatchIssues(t, valid)

	invalid := map[string]any{
		"watches": map[string]any{
			"postgres-policy": map[string]any{
				"policy": map[string]any{"cooldown": "1m"},
				"check": map[string]any{
					"type": checks.CheckTypeProcessPolicy,
					"allow": map[string]any{
						"bad": map[string]any{
							"exe":   "../postgres",
							"cmd":   "postgres",
							"extra": true,
						},
					},
				},
				"then": map[string]any{
					"hook": map[string]any{"command": []any{"/bin/false"}},
					"kill": map[string]any{},
				},
			},
		},
	}
	assertWatchIssues(t, invalid,
		"check.user is required for a process_policy check",
		"check.allow.bad.exe must be a clean absolute resolved executable path",
		"check.allow.bad.cmd must be anchored with ^ and $",
		"check.allow.bad.extra is not supported",
		"policy is not valid on an alert-only process_policy watch",
		"then.hook is not valid on an alert-only process_policy watch",
		"then.kill is not valid on an alert-only process_policy watch",
	)
}

func TestValidateRaidNotifyOn(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
		"notifiers": map[string]any{"ops": map[string]any{"type": "wall"}},
		"watches": map[string]any{"raid-md0": map[string]any{
			"check": map[string]any{"type": "raid", "array": "md0", "sysfs_changes": true},
			"then":  map[string]any{"notify": []any{"ops"}, "notify_on": []any{"on_degraded", "on_array_change"}},
		}},
	})
	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{"load": map[string]any{
			"check": map[string]any{"type": "load", "load1": map[string]any{"op": ">", "value": 1}},
			"then":  map[string]any{"notify_on": []any{"on_change"}, "notify": []any{"none"}},
		}},
	},
		"only valid on a raid or lvm watch")
}

func TestValidateRAIDControl(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{"raid-md0": map[string]any{
			"check":        map[string]any{"type": "raid", "array": "md0"},
			"raid_control": map[string]any{"pause_resume": true},
			"then":         map[string]any{"notify": []any{"none"}},
		}},
	})
	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{"raid": map[string]any{
			"check":        map[string]any{"type": "raid"},
			"raid_control": map[string]any{"pause_resume": true},
			"then":         map[string]any{"notify": []any{"none"}},
		}},
	},
		"requires check.array")
}

// assertWatchNotifyIntervalIssue validates a hook-only storage watch with the
// given notify_interval and asserts a watch issue containing wantSubstr.
func assertWatchNotifyIntervalIssue(t *testing.T, interval, wantSubstr string) {
	t.Helper()
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"storage-root": map[string]any{
				"monitor": "previous",
				"check":   map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"then": map[string]any{
					"hook":            map[string]any{"command": []any{"/usr/local/bin/alert.sh"}},
					"notify_interval": interval,
				},
			},
		},
	})
	if !hasIssueContaining(watchIssues(issues), wantSubstr) {
		t.Fatalf("expected an issue containing %q, got %v", wantSubstr, watchIssues(issues))
	}
}

func TestValidateWatchesNotifyIntervalBadDuration(t *testing.T) {
	assertWatchNotifyIntervalIssue(t, "soon", "notify_interval")
}

func TestValidateWatchesNotifyIntervalWithoutTargets(t *testing.T) {
	assertWatchNotifyIntervalIssue(t, "30m", "no effect without notify targets")
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
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
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

	assertWatchIssues(t, watchConfigs(map[string]any{
		"metric":  map[string]any{"type": "metric", "name": "cpu", "op": ">", "value": "90"},
		"service": map[string]any{"type": "service", "expect": "active"},
	}),
		`watches.metric.check.type "metric" is not supported`,
		`watches.service.check.type "service" is not supported`)
}

func TestValidateClockWatch(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"clock-drift": map[string]any{
				"check": map[string]any{
					"type":                "clock",
					"servers":             []any{"time.cloudflare.com", "pool.ntp.org"},
					"max_offset":          "2s",
					"max_stratum":         4,
					"max_root_dispersion": "250ms",
				},
				"for":  map[string]any{"cycles": 2},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/sbin/sync-clock"}}},
			},
		},
	})

	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"clock-drift": map[string]any{
				"check": map[string]any{
					"type":                "clock",
					"servers":             42,
					"max_offset":          "soon",
					"max_stratum":         16,
					"max_root_dispersion": "0s",
					"port":                99999,
				},
			},
		},
	},
		"servers", "max_offset", "max_stratum", "max_root_dispersion", "port")
}

func TestValidateClockWatchSource(t *testing.T) {
	// source: chrony reads the local daemon, so it takes host/port or socket
	// instead of a remote server list.
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"chrony-drift": map[string]any{
				"check": map[string]any{
					"type":                "clock",
					"source":              "chrony",
					"max_offset":          "1s",
					"max_stratum":         4,
					"max_root_dispersion": "200ms",
				},
			},
			"chrony-drift-socket": map[string]any{
				"check": map[string]any{
					"type":       "clock",
					"source":     "chrony",
					"socket":     "/run/chrony/chronyd.sock",
					"max_offset": "1s",
				},
			},
			"ntp-drift-explicit": map[string]any{
				"check": map[string]any{
					"type":       "clock",
					"source":     "ntp",
					"servers":    []any{"pool.ntp.org"},
					"max_offset": "2s",
				},
			},
		},
	})

	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"bad-source": map[string]any{
				"check": map[string]any{"type": "clock", "source": "timesyncd", "max_offset": "1s"},
			},
		},
	}, "source")

	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"chrony-with-servers": map[string]any{
				"check": map[string]any{
					"type":       "clock",
					"source":     "chrony",
					"servers":    []any{"pool.ntp.org"},
					"max_offset": "1s",
				},
			},
		},
	}, "servers")

	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"ntp-with-socket": map[string]any{
				"check": map[string]any{
					"type":       "clock",
					"servers":    []any{"pool.ntp.org"},
					"socket":     "/run/chrony/chronyd.sock",
					"max_offset": "2s",
				},
			},
		},
	}, "socket")

	// max_offset stays required whichever source is configured.
	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"chrony-no-threshold": map[string]any{
				"check": map[string]any{"type": "clock", "source": "chrony"},
			},
		},
	}, "max_offset")
}

func TestValidateChronyWatch(t *testing.T) {
	// The chrony protocol check validates through the shared conn path, so it
	// needs no validator of its own.
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"chronyd": map[string]any{
				"check": map[string]any{
					"type": "chrony",
					"host": "127.0.0.1",
					"port": 323,
					"expect": map[string]any{
						"synchronized":   "true",
						"sources_online": map[string]any{"op": ">=", "value": 3},
					},
				},
			},
			"chronyd-socket": map[string]any{
				"check": map[string]any{"type": "chrony", "socket": "/run/chrony/chronyd.sock"},
			},
		},
	})

	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"chronyd": map[string]any{
				"check": map[string]any{"type": "chrony", "port": 99999},
			},
		},
	}, "port")
}

func TestValidateFileWatchGood(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"app-data": map[string]any{
				"check": map[string]any{
					"type":           "file",
					"paths":          []any{"/var/lib/app", "/srv/app"},
					"recursive":      true,
					"include_hidden": true,
					"older_than":     "24h",
					"size":           map[string]any{"op": ">", "value": 1048576},
					"permissions":    map[string]any{"on": "change"},
					"owner":          map[string]any{"on": "change"},
					"existence":      map[string]any{"on": "delete"},
				},
				"then": map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/file.sh"}}},
			},
		},
	})
}

func TestValidateFileWatchErrors(t *testing.T) {
	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"no-cond": map[string]any{
				"check": map[string]any{"type": "file", "paths": []any{"/x"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-size": map[string]any{
				"check": map[string]any{"type": "file", "paths": []any{"/x"}, "size": map[string]any{"op": "><", "value": "big"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-perm": map[string]any{
				"check": map[string]any{"type": "file", "paths": []any{"/x"}, "permissions": map[string]any{"on": "touch"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-exist": map[string]any{
				"check": map[string]any{"type": "file", "paths": []any{"/x"}, "existence": map[string]any{"on": "create"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"no-paths": map[string]any{
				"check": map[string]any{"type": "file", "size": map[string]any{"on": "change"}},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-older-than": map[string]any{
				"check": map[string]any{"type": "file", "paths": []any{"/x"}, "older_than": "soon"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
			"bad-include-hidden": map[string]any{
				"check": map[string]any{"type": "file", "paths": []any{"/x"}, "older_than": "1h", "include_hidden": "yes"},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x.sh"}}},
			},
		},
	},
		"watches.no-cond.check requires at least one of size, permissions, owner, existence, older_than",
		"watches.bad-size.check.size requires on: change or {op, value}",
		"watches.bad-perm.check.permissions requires on: change",
		"watches.bad-exist.check.existence requires on: delete",
		"watches.no-paths.check: file check paths is required",
		"watches.bad-older-than.check.older_than must be a valid positive duration",
		"watches.bad-include-hidden.check.include_hidden must be a boolean")
}

func TestValidateProcessWatchGood(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidateProcessWatchGoneOnly(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"liveness": map[string]any{
				"check": map[string]any{"type": "process", "name": "nginx", "gone": true},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/usr/local/bin/down.sh"}}},
			},
		},
	})
}

func TestValidateProcessWatchErrors(t *testing.T) {
	assertWatchIssues(t, map[string]any{
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
	},
		"watches.no-name.check.name is required for a process check",
		"watches.no-cond.check requires at least one of for, cpu, memory, io",
		"watches.bad-for.check.for \"soon\" must be a valid positive duration",
		"watches.bad-cpu.check.cpu requires {op, value} with a numeric value")
}

func TestValidateProcessWatchKillGood(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidateProcessWatchKillErrors(t *testing.T) {
	assertWatchIssues(t, map[string]any{
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
	},
		"watches.bad-signal.then.kill.signal \"HUP\" must be TERM or KILL",
		"watches.bad-escalate.then.kill.escalate must be a boolean",
		"watches.bad-timeout.then.kill.term_timeout \"soon\" must be a valid positive duration",
		"watches.basename-kill.then.kill requires check.name to be an absolute resolved exe path",
		"watches.missing-user-kill.then.kill requires check.user",
		"watches.kill-on-storage.then.kill is only valid on a process watch")
}

func TestValidateStorageInodesWatch(t *testing.T) {
	assertNoWatchIssues(t, watchConfig("storage-inodes", map[string]any{
		"type":            "storage",
		"path":            "/",
		"inodes_used_pct": map[string]any{"op": ">=", "value": 90},
		"inodes_free":     map[string]any{"op": "<", "value": 10000},
	}))

	assertWatchIssues(t, watchConfig("storage-inodes", map[string]any{"type": "storage", "path": "/", "inodes_used_pct": map[string]any{"op": "=>", "value": "lots"}}),
		"watches.storage-inodes.check.inodes_used_pct has an invalid op")
}

func TestValidateStorageBytePredicates(t *testing.T) {
	assertNoWatchIssues(t, watchConfig("storage-bytes", map[string]any{
		"type":       "storage",
		"path":       "/",
		"free_bytes": map[string]any{"op": "<", "value": "10G"},
		"used_bytes": map[string]any{"op": ">=", "value": "100G"},
	}))

	assertNoWatchIssues(t, watchConfig("storage-percent", map[string]any{
		"type":     "storage",
		"path":     "/",
		"used_pct": map[string]any{"op": ">=", "value": "90%"},
	}))

	assertWatchIssues(t, watchConfig("storage-bytes", map[string]any{"type": "storage", "path": "/", "free_bytes": map[string]any{"op": "<", "value": "lots"}}),
		"watches.storage-bytes.check.free_bytes value \"lots\" must include a size suffix")

	assertWatchIssues(t, watchConfig("storage-bytes", map[string]any{"type": "storage", "path": "/", "free_bytes": map[string]any{"op": "<", "value": 10}}),
		"watches.storage-bytes.check.free_bytes value \"10\" must include a size suffix")
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
	mustNotHave(t, good, "notifiers.")

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
		"notifiers.bad-type.type \"smoke-signal\" is not supported (email, gotify, ntfy, slack, teams, telegram, tty, wall)",
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

	assertNoWatchIssues(t, map[string]any{
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
	if !hasIssue(noDefault, "watches.no-action.then requires a hook, notify, kill, expand and/or makestep") {
		t.Fatalf("expected empty then without global notify to fail, got %v", noDefault)
	}
	if hasIssue(noDefault, "watches.dry-run-only") {
		t.Fatalf("dry_run top-level must be valid without actions, got %v", noDefault)
	}

	// Bare watch (no "then" key at all) with check+for is valid as alert-only:
	// produces firing events / web state but no actions (even if globals exist).
	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidateServiceCheckAsWatch(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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

	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"load": map[string]any{
				"check": map[string]any{"type": "load", "load1": map[string]any{"op": ">", "value": 8}},
				"then":  map[string]any{"expand": map[string]any{"by": "5G"}},
			},
		},
	},
		"watches.load.then.expand is only valid on a storage watch")

	assertWatchIssues(t, watchConfigs(map[string]any{
		"no-url": map[string]any{"type": "http"},
		"weird":  map[string]any{"type": "definitely-not-a-check"},
	}),
		"watches.no-url.check.url is required for an http check",
		"watches.weird.check.type \"definitely-not-a-check\" is not supported")
}

func assertRequiredWatchPredicate(t *testing.T, name, checkType string, goodCheck map[string]any, wantIssue string) {
	t.Helper()
	good := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			name: map[string]any{
				"check": goodCheck,
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if w := watchIssues(good); len(w) != 0 {
		t.Fatalf("expected no watch issues, got %v", w)
	}

	bad := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{
			"missing": map[string]any{
				"check": map[string]any{"type": checkType},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
	if !hasIssue(bad, wantIssue) {
		t.Fatalf("expected missing-predicate issue %q, got %v", wantIssue, bad)
	}
}

func TestValidateZombiesWatch(t *testing.T) {
	assertRequiredWatchPredicate(t, "zombies", "zombies", map[string]any{"type": "zombies", "count": map[string]any{"op": ">", "value": 20}}, "watches.missing.check requires at least one of count")
}

func TestValidatePortsWatch(t *testing.T) {
	assertNoWatchIssues(t, watchConfig("scan", map[string]any{"type": "ports", "host": "10.0.0.1", "ports": "22,80,443", "match": "all"}))

	assertWatchIssues(t, watchConfig("scan", map[string]any{"type": "ports", "ports": "bad"}),
		`watches.scan.check.ports has an invalid port`)
}

func TestValidateCertWatch(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"api-cert": map[string]any{
				"check": map[string]any{"type": "cert", "host": "api.example.com", "expires_in_days": 14, "on_issuer_change": true},
				"then":  map[string]any{"notify": []any{"x"}},
			},
		},
		"notifiers": map[string]any{"x": map[string]any{"type": "slack", "webhook": "https://h/x"}},
	})

	assertWatchIssues(t, watchConfig("c", map[string]any{"type": "cert"}),
		"watches.c.check requires a host or a path")
}

func TestValidateReplicationWatch(t *testing.T) {
	assertNoWatchIssues(t, watchConfigs(map[string]any{
		"db-replication": map[string]any{
			"type": "replication", "user": "root",
			"behind": map[string]any{"op": "<", "value": 300},
		},
	}))
	assertWatchIssues(t, watchConfigs(map[string]any{
		"no-user": map[string]any{"type": "replication"},
		"bad-eng": map[string]any{"type": "replication", "user": "root", "engine": "postgres"},
		"bad-lag": map[string]any{"type": "replication", "user": "root", "behind": map[string]any{"op": "<>", "value": "x"}},
	}),
		"watches.no-user.check.user is required for a replication check",
		"watches.bad-eng.check.engine must be mysql or mariadb for a replication check",
		"watches.bad-lag.check.behind.op \"<>\" is not one of",
		"watches.bad-lag.check.behind.value must be numeric")
}

func TestValidateReplicationControlBlock(t *testing.T) {
	good := map[string]any{
		"check":               map[string]any{"type": "replication", "user": "root"},
		"replication_control": map[string]any{"start": true},
	}
	assertNoWatchIssues(t, map[string]any{"watches": map[string]any{"db-replication": good}})

	assertWatchIssues(t, map[string]any{"watches": map[string]any{
		"on-raid": map[string]any{
			"check":               map[string]any{"type": "raid"},
			"replication_control": map[string]any{"start": true},
		},
		"bad-key": map[string]any{
			"check":               map[string]any{"type": "replication", "user": "root"},
			"replication_control": map[string]any{"stop": true},
		},
	}},
		"watches.on-raid.replication_control is only valid on a replication watch",
		"watches.bad-key.replication_control.stop is not supported",
		"watches.bad-key.replication_control.start is required")
}

func TestValidateStorageMountWatch(t *testing.T) {
	// A storage watch can carry a mount condition (mount + space in one entry).
	assertNoWatchIssues(t, watchConfig("data-mount", map[string]any{"type": "storage", "path": "/data", "mounted": true}))

	assertWatchIssues(t, watchConfigs(map[string]any{
		"m": map[string]any{"type": "storage", "path": "/data"}, // no predicate, no mount condition
		"unsupported-mount-controls": map[string]any{
			"type":    "storage",
			"path":    "/data",
			"mounted": true,
			"fstype":  "ext4",
			"device":  "/dev/sdb1",
			"options": []any{"rw"},
		},
	}),
		"watches.m.check requires a space/inode predicate",
		"watches.unsupported-mount-controls.check.fstype is not supported for a storage check",
		"watches.unsupported-mount-controls.check.device is not supported for a storage check",
		"watches.unsupported-mount-controls.check.options is not supported for a storage check")
}

func TestValidateConntrackWatch(t *testing.T) {
	assertRequiredWatchPredicate(t, "conntrack", "conntrack", map[string]any{"type": "conntrack", "used_pct": map[string]any{"op": ">=", "value": 90}}, "watches.missing.check requires at least one of used_pct/free/count")
}

func TestValidateFdsWatch(t *testing.T) {
	assertNoWatchIssues(t, watchConfig("fds", map[string]any{
		"type":     "fds",
		"used_pct": map[string]any{"op": ">=", "value": 85},
		"free":     map[string]any{"op": "<", "value": 10000},
	}))

	assertWatchIssues(t, watchConfigs(map[string]any{
		"no-pred": map[string]any{"type": "fds"},
		"bad-op":  map[string]any{"type": "fds", "used_pct": map[string]any{"op": "=>", "value": "lots"}},
	}),
		"watches.no-pred.check requires at least one of used_pct/free/allocated",
		"watches.bad-op.check.used_pct has an invalid op",
		"watches.bad-op.check.used_pct value \"lots\" must be a percentage in 0..100")
}

func TestValidateOomWatch(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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

	assertWatchIssues(t, watchConfig("oom", map[string]any{"type": "oom", "delta": map[string]any{"op": "=>", "value": "many"}}),
		"watches.oom.check.delta has an invalid op",
		"watches.oom.check.delta value \"many\" must be numeric")
}

func TestValidateLoadWatch(t *testing.T) {
	assertNoWatchIssues(t, watchConfig("load", map[string]any{
		"type":    "load",
		"per_cpu": true,
		"load5":   map[string]any{"op": ">", "value": 1.0},
		"load15":  map[string]any{"op": ">", "value": 0.8},
	}))

	assertWatchIssues(t, watchConfig("no-pred", map[string]any{"type": "load", "per_cpu": "yes"}),
		"watches.no-pred.check.per_cpu must be a boolean",
		"watches.no-pred.check requires at least one of load1/load5/load15")
}

func TestValidateSwapWatchGood(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidateSwapWatchErrors(t *testing.T) {
	assertWatchIssues(t, map[string]any{
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
	},
		"watches.no-metrics.metrics is required and must be non-empty for a swap check",
		"watches.empty-usage.metrics.usage requires at least one of used_pct/free_pct/free_bytes",
		"watches.bad-io.metrics.io.delta has an invalid op",
		"watches.bad-metric.metrics.bogus is not a supported swap metric (usage or io)")
}

func TestValidateWatchesGoodForWindow(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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
	assertEachWatchInvalid(t, "w", cases)
}

func TestValidateWatchesMessageMentionsName(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"watches": map[string]any{"storage-root": map[string]any{"check": map[string]any{"type": "storage"}}},
	})
	var joined strings.Builder
	for _, i := range watchIssues(issues) {
		joined.WriteString(i.Msg)
	}
	if !strings.Contains(joined.String(), "storage-root") {
		t.Fatalf("issue should name the watch: %v", issues)
	}
}

func TestValidateWatchesNetGood(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidateWatchesNetBad(t *testing.T) {
	hook := map[string]any{"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}}
	merge := func(m map[string]any) map[string]any {
		out := map[string]any{}
		maps.Copy(out, hook)
		maps.Copy(out, m)
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
	assertEachWatchInvalid(t, "net-eth0", cases)
}

func TestValidateWatchesICMPGood(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidateWatchesICMPBad(t *testing.T) {
	hook := map[string]any{"then": map[string]any{"hook": map[string]any{"command": []any{"/x"}}}}
	merge := func(m map[string]any) map[string]any {
		out := map[string]any{}
		maps.Copy(out, hook)
		maps.Copy(out, m)
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
	assertEachWatchInvalid(t, "ping-gw", cases)
}

func TestValidateWatchPolicy(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"grow": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/data", "free_pct": map[string]any{"op": "<", "value": 10}},
				"policy": map[string]any{"cooldown": "30m"},
				"then":   map[string]any{"expand": map[string]any{"by": "5G"}},
			},
		},
	})

	assertWatchIssues(t, map[string]any{
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
	},
		`watches.bad-cooldown.policy.cooldown "-5m" must be a valid positive duration`,
		"watches.bad-shape.policy must be a mapping",
		"watches.bad-actions.policy.max_actions requires policy.max_actions_window")
}

func TestValidateExpandBy(t *testing.T) {
	storageCheck := map[string]any{"type": "storage", "path": "/data", "free_pct": map[string]any{"op": "<", "value": 10}}
	assertWatchIssues(t, map[string]any{
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
	},
		`watches.no-by.then.expand.by "" must be a positive size with a K/M/G/T suffix`,
		`watches.unitless.then.expand.by "1024" must be a positive size with a K/M/G/T suffix`,
		"watches.bad-shape.then.expand must be a mapping with a `by` size")
}

func TestValidateSwapUsageSharedGrammar(t *testing.T) {
	// Percent and byte-size forms work in swap usage exactly like in storage
	// (section: unified checks — one predicate grammar for every level check).
	assertNoWatchIssues(t, map[string]any{
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

	assertWatchIssues(t, map[string]any{
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
	},
		`used_pct value "150%" must be a percentage in 0..100`,
		`free_bytes value "1024" must include a size suffix`)
}

func TestValidateMetricWatchEntryLevelBlocks(t *testing.T) {
	// then/for/within on a multi-metric watch entry belong in each metric's own
	// block, so validation must reject entry-level copies.
	assertWatchIssues(t, map[string]any{
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
	},
		"watches.swap.then is not valid on a multi-metric watch",
		"watches.swap.for is not valid on a multi-metric watch",
		"watches.net.then is not valid on a multi-metric watch")

	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidateWithinMinMatchesOptional(t *testing.T) {
	// min_matches defaults to 1; only an explicit invalid value is an error.
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"root": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"within": map[string]any{"cycles": 5},
				"then":   map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
}

func TestValidateWatchWithoutCheckStillValidatesEntry(t *testing.T) {
	// A missing check must not mask the entry-level problems: everything is
	// reported in one validation pass.
	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"broken": map[string]any{
				"interval": "soon",
				"within":   map[string]any{"cycles": 0},
				"policy":   map[string]any{"cooldown": "-1m"},
				"then":     map[string]any{"notify": []any{"ghost"}},
			},
		},
	},
		"watches.broken.check is required",
		`watches.broken.interval "soon" must be a valid positive duration`,
		"watches.broken.within.cycles must be > 0",
		`watches.broken.policy.cooldown "-1m" must be a valid positive duration`,
		`watches.broken.then.notify references unknown notifier "ghost"`)
}

func TestValidateScalarWithinRejectedOnWatch(t *testing.T) {
	assertWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"root": map[string]any{
				"check":  map[string]any{"type": "storage", "path": "/", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"within": "1h",
				"then":   map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	},
		"watches.root.within must be a mapping")
}

func TestValidateMemoryWatch(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidatePressureWatch(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidatePidsWatch(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"pid-table": map[string]any{
				"check": map[string]any{"type": "pids", "used_pct": map[string]any{"op": ">=", "value": 90}},
				"for":   map[string]any{"cycles": 3},
				"then":  map[string]any{"hook": map[string]any{"command": []any{"/x"}}},
			},
		},
	})
}

func TestValidateDiskIOWatch(t *testing.T) {
	assertNoWatchIssues(t, map[string]any{
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
}

func TestValidateWatchPortRangeMatchesServices(t *testing.T) {
	// A tcp/connection check used as a watch enforces the same 1..65535 port
	// range walkScalars applies to resolved services.
	assertWatchIssues(t, watchConfigs(map[string]any{
		"tcp-high":  map[string]any{"type": "tcp", "port": 99999},
		"conn-high": map[string]any{"type": "smtp", "host": "127.0.0.1", "port": 99999},
	}),
		"watches.tcp-high.check.port is required and must be a port in 1..65535",
		`watches.conn-high.check.port "99999" must be an integer in 1..65535`)
}

func TestValidateDBusWatchTarget(t *testing.T) {
	assertNoWatchIssues(t, watchConfig("libvirt-dbus", map[string]any{
		"type":           "dbus",
		"bus_name":       "org.libvirt",
		"object_path":    "/org/libvirt",
		"probe":          "property",
		"dbus_interface": "org.libvirt.Connect",
		"property":       "Version",
		"require_owner":  true,
	}))

	tests := []struct {
		name  string
		check map[string]any
		want  string
	}{
		{name: "missing object path", check: map[string]any{"type": "dbus", "bus_name": "org.libvirt"}, want: "object_path is required"},
		{name: "missing bus name", check: map[string]any{"type": "dbus", "object_path": "/org/libvirt"}, want: "bus_name is required"},
		{name: "empty bus name", check: map[string]any{"type": "dbus", "bus_name": "", "object_path": "/org/libvirt"}, want: "bus_name must be a non-empty string"},
		{name: "non-string path", check: map[string]any{"type": "dbus", "bus_name": "org.libvirt", "object_path": 42}, want: "object_path must be a non-empty string"},
		{name: "invalid bus name", check: map[string]any{"type": "dbus", "bus_name": ":1.42", "object_path": "/org/libvirt"}, want: "not a valid well-known D-Bus name"},
		{name: "invalid object path", check: map[string]any{"type": "dbus", "bus_name": "org.libvirt", "object_path": "org/libvirt"}, want: "not a valid D-Bus object path"},
		{name: "empty probe", check: map[string]any{"type": "dbus", "bus_name": "org.libvirt", "object_path": "/org/libvirt", "probe": ""}, want: "probe must be a non-empty string"},
		{name: "non-string interface", check: map[string]any{"type": "dbus", "bus_name": "org.libvirt", "object_path": "/org/libvirt", "probe": "introspect", "dbus_interface": 42}, want: "dbus_interface must be a non-empty string"},
		{name: "unknown probe", check: map[string]any{"type": "dbus", "bus_name": "org.libvirt", "object_path": "/org/libvirt", "probe": "call"}, want: "probe must be"},
		{name: "property missing interface", check: map[string]any{"type": "dbus", "bus_name": "org.libvirt", "object_path": "/org/libvirt", "probe": "property", "property": "Version"}, want: "dbus_interface is required"},
		{name: "property missing property", check: map[string]any{"type": "dbus", "bus_name": "org.libvirt", "object_path": "/org/libvirt", "probe": "property", "dbus_interface": "org.libvirt.Connect"}, want: "property is required"},
		{name: "owner required without target", check: map[string]any{"type": "dbus", "require_owner": true}, want: "bus_name is required when require_owner is set"},
		{name: "owner required not boolean", check: map[string]any{"type": "dbus", "bus_name": "org.libvirt", "object_path": "/org/libvirt", "require_owner": "yes"}, want: "require_owner must be a boolean"},
		{name: "field on another protocol", check: map[string]any{"type": "smtp", "bus_name": "org.libvirt"}, want: "bus_name is only supported for a dbus check"},
		{name: "probe on another protocol", check: map[string]any{"type": "smtp", "probe": "peer"}, want: "probe is only supported for a dbus check"},
		{name: "interface on another protocol", check: map[string]any{"type": "smtp", "dbus_interface": "org.libvirt.Connect"}, want: "dbus_interface is only supported for a dbus check"},
		{name: "property on another protocol", check: map[string]any{"type": "smtp", "property": "Version"}, want: "property is only supported for a dbus check"},
		{name: "owner required on another protocol", check: map[string]any{"type": "smtp", "require_owner": true}, want: "require_owner is only supported for a dbus check"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertWatchIssues(t, watchConfig("target", test.check), test.want)
		})
	}
}

func TestValidateFileProcessWatchRejectsEntryLevelWindow(t *testing.T) {
	assertWatchIssues(t, map[string]any{
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
	},
		"watches.cfg.for is not valid on a file watch",
		"watches.proc.within is not valid on a process watch")
}

func TestValidateWatchMakeStepAction(t *testing.T) {
	clockCheck := map[string]any{"type": "clock", "source": "chrony", "max_offset": "5s"}

	// The shipped shape: a clock watch with a positive cooldown.
	assertNoWatchIssues(t, map[string]any{
		"watches": map[string]any{
			"clock-step": map[string]any{
				"check":  clockCheck,
				"policy": map[string]any{"cooldown": "30m"},
				"then": map[string]any{
					"makestep": map[string]any{"socket": "/run/chrony/chronyd.sock"},
				},
			},
			"clock-step-default-socket": map[string]any{
				"check":  clockCheck,
				"policy": map[string]any{"cooldown": "30m"},
				"then":   map[string]any{"makestep": map[string]any{}},
			},
		},
	})

	cases := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{
			name: "requires a cooldown",
			entry: map[string]any{
				"check": clockCheck,
				"then":  map[string]any{"makestep": map[string]any{}},
			},
			want: "policy.cooldown",
		},
		{
			name: "rejects a zero cooldown",
			entry: map[string]any{
				"check":  clockCheck,
				"policy": map[string]any{"cooldown": "0s"},
				"then":   map[string]any{"makestep": map[string]any{}},
			},
			want: "policy.cooldown",
		},
		{
			name: "only on a clock watch",
			entry: map[string]any{
				"check":  map[string]any{"type": "http", "url": "http://example.test"},
				"policy": map[string]any{"cooldown": "30m"},
				"then":   map[string]any{"makestep": map[string]any{}},
			},
			want: "only valid on a clock watch",
		},
		{
			name: "must be a mapping",
			entry: map[string]any{
				"check":  clockCheck,
				"policy": map[string]any{"cooldown": "30m"},
				"then":   map[string]any{"makestep": true},
			},
			want: "must be a mapping",
		},
		{
			// chronyd's UDP port refuses privileged commands, so a host/port form
			// cannot work — reject it rather than silently ignore it.
			name: "rejects a host/port form",
			entry: map[string]any{
				"check":  clockCheck,
				"policy": map[string]any{"cooldown": "30m"},
				"then": map[string]any{
					"makestep": map[string]any{"host": "127.0.0.1", "port": 323},
				},
			},
			want: "makestep.host",
		},
		{
			name: "socket must be absolute",
			entry: map[string]any{
				"check":  clockCheck,
				"policy": map[string]any{"cooldown": "30m"},
				"then":   map[string]any{"makestep": map[string]any{"socket": "chronyd.sock"}},
			},
			want: "absolute path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertWatchIssues(t, map[string]any{
				"watches": map[string]any{"clock-step": tc.entry},
			}, tc.want)
		})
	}
}
