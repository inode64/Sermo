package config

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/rules"
	"sermo/internal/servicemgr"
)

// The logind profile's guard sensors: sessions attached to a local seat, and
// users a restart could leave without their manager.
const (
	seatedSessions     = "seated-sessions"
	usersWithoutLinger = "users-without-linger"
)

// Stop and restart wait while a session sits on a local seat (logind may
// release its device descriptors) or a user without linger is logged in (a
// restarted logind may drop its sessions and stop that user's manager: on
// 12-nov-2026 that killed a month-old tmux), and while either cannot be read.
// Both sensors read logind's records from disk, so the guards answer while
// logind has lost the system bus, and they run no command. Start stays
// available.
func TestCatalogLogindGuards(t *testing.T) {
	t.Parallel()
	resolved := resolveCatalogService(t, "systemd-logind", "systemd")
	for sensor, wantType := range map[string]string{
		seatedSessions:     checks.CheckTypeSeatedSessions,
		usersWithoutLinger: checks.CheckTypeUsersWithoutLinger,
	} {
		check := nested(t, resolved.Tree, "checks", sensor)
		if cfgval.String(check["type"]) != wantType {
			t.Fatalf("%s check = %v, want type %s", sensor, check, wantType)
		}
	}
	parsed, warnings := rules.ParseRules(resolved.Tree)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	clear := func(name string) checks.Result { return checks.Result{Check: name} }
	for _, tc := range []struct {
		name               string
		seated, unlingered checks.Result
		guard              string
	}{
		{"nothing at risk", clear(seatedSessions), clear(usersWithoutLinger), ""},
		{"seated session", checks.Result{Check: seatedSessions, OK: true}, clear(usersWithoutLinger), "block-stop-during-local-sessions"},
		{"sessions unreadable", checks.Result{Check: seatedSessions, Unavailable: true}, clear(usersWithoutLinger), "block-stop-during-local-sessions"},
		{"user without linger", clear(seatedSessions), checks.Result{Check: usersWithoutLinger, OK: true}, "block-restart-while-users-unlingered"},
		{"users unreadable", clear(seatedSessions), checks.Result{Check: usersWithoutLinger, Unavailable: true}, "block-restart-while-users-unlingered"},
	} {
		for _, action := range []rules.ActionType{rules.ActionStop, rules.ActionRestart, rules.ActionStart} {
			t.Run(tc.name+"/"+string(action), func(t *testing.T) {
				t.Parallel()
				runner := execxtest.Fixed(execx.Result{}, nil)
				ev := &rules.Evaluator{
					Cache: map[string]checks.Result{seatedSessions: tc.seated, usersWithoutLinger: tc.unlingered},
					Deps:  checks.Deps{Runner: runner, DefaultTimeout: time.Second},
				}
				name, reason, err := rules.Guard(t.Context(), parsed, string(action), ev)
				want := tc.guard != "" && action != rules.ActionStart
				if blocked := name != "" || err != nil; blocked != want {
					t.Fatalf("blocked=%v, want %v: guard=%q reason=%q err=%v", blocked, want, name, reason, err)
				}
				if want && err == nil && name != tc.guard {
					t.Fatalf("blocked by %q, want %q", name, tc.guard)
				}
				if calls := runner.Calls(); len(calls) != 0 {
					t.Fatalf("logind guards must not run commands: %v", calls)
				}
			})
		}
	}
}

// A logind left without login1 after a bus restart is restarted, but only in
// that state: a stopped unit or a dead bus skips the probe, so neither counts
// toward the restart.
func TestCatalogLogindRestartsWhenLogin1Unowned(t *testing.T) {
	t.Parallel()
	resolved := resolveCatalogService(t, "systemd-logind", "systemd")
	if got := cfgval.StringList(nested(t, resolved.Tree, "checks", "login1")["requires"]); !slices.Equal(got, []string{"service", "bus"}) {
		t.Fatalf("login1 requires = %v, want [service bus]", got)
	}
	parsed, warnings := rules.ParseRules(resolved.Tree)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	idx := slices.IndexFunc(parsed, func(r rules.Rule) bool { return r.Name == "login1" })
	if idx < 0 {
		t.Fatal("login1 remediation rule missing")
	}
	rule := parsed[idx]
	if action, ok := rule.Operation(); !ok || action.Type != rules.ActionRestart {
		t.Fatalf("login1 operation = %+v (ok=%v), want restart", action, ok)
	}
	for _, tc := range []struct {
		name   string
		result checks.Result
		want   bool
	}{
		{"owned", checks.Result{Check: "login1", OK: true}, false},
		{"unowned", checks.Result{Check: "login1"}, true},
		{"unit or bus down", checks.Result{Check: "login1", Skipped: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ev := &rules.Evaluator{Cache: map[string]checks.Result{"login1": tc.result}}
			var window rules.WindowState
			at := time.Unix(1_700_000_000, 0)
			fired := false
			for cycle := range 3 {
				active, err := ev.EvalRule(t.Context(), rule)
				if err != nil {
					t.Fatal(err)
				}
				fired = window.FiresAt(rule, active, at.Add(time.Duration(cycle)*time.Minute))
				if fired && cycle < 2 {
					t.Fatalf("login1 restart fired on cycle %d, before its three-cycle window", cycle+1)
				}
			}
			if fired != tc.want {
				t.Fatalf("login1 restart fired=%v, want %v", fired, tc.want)
			}
		})
	}
}

// Under systemd a running bus is never restarted: logind and the other clients
// keep their dead connection. A failed bus may still be started, and hosts not
// booted with systemd keep the operation.
func TestCatalogDBusGuardsRestartUnderSystemd(t *testing.T) {
	t.Parallel()
	booted := t.TempDir()
	for _, tc := range []struct {
		name    string
		marker  string
		running bool
		blocked bool
	}{
		{"systemd with running bus", booted, true, true},
		{"systemd with failed bus", booted, false, false},
		{"not booted with systemd", filepath.Join(booted, "absent"), true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			global := writeConfig(t, map[string]string{
				"sermo.yml":         "engine: {backend: systemd}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
				"services/dbus.yml": "name: dbus\nuses: dbus\nvariables: {systemd_booted_path: " + tc.marker + "}\n",
			})
			cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))))
			if err != nil {
				t.Fatal(err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatal(issues)
			}
			resolved, errs := cfg.Resolve("dbus")
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			parsed, warnings := rules.ParseRules(resolved.Tree)
			if len(warnings) != 0 {
				t.Fatal(warnings)
			}
			state := servicemgr.StatusFailed
			if tc.running {
				state = servicemgr.StatusActive
			}
			status := func(context.Context) (servicemgr.Status, error) { return state, nil }
			for _, action := range []rules.ActionType{rules.ActionStop, rules.ActionRestart, rules.ActionStart} {
				ev := &rules.Evaluator{Deps: checks.Deps{Status: status, DefaultTimeout: time.Second}}
				name, reason, err := rules.Guard(t.Context(), parsed, string(action), ev)
				want := tc.blocked && action != rules.ActionStart
				if blocked := name != "" || err != nil; blocked != want {
					t.Fatalf("%s: blocked=%v, want %v: guard=%q reason=%q err=%v", action, blocked, want, name, reason, err)
				}
			}
		})
	}
}

func TestCatalogMachinedRestartDoesNotQueryRegisteredMachines(t *testing.T) {
	t.Parallel()
	resolved := resolveCatalogService(t, "systemd-machined", "systemd")
	parsed, warnings := rules.ParseRules(resolved.Tree)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	runner := execxtest.Fixed(execx.Result{Stdout: `a(ssso) 2 ".host" "host" "" "/org/freedesktop/machine1/machine/_2ehost" "fixture" "vm" "libvirt-qemu" "/org/freedesktop/machine1/machine/fixture"`}, nil)
	ev := &rules.Evaluator{Deps: checks.Deps{Runner: runner, DefaultTimeout: time.Second}}
	name, reason, err := rules.Guard(t.Context(), parsed, string(rules.ActionRestart), ev)
	if name != "" || err != nil || len(runner.Calls()) != 0 {
		t.Fatalf("registered guests must not prevent restarting their manager: guard=%q reason=%q err=%v calls=%v", name, reason, err, runner.Calls())
	}
}

func TestCatalogManagerRestartRetainsOperatorGuards(t *testing.T) {
	t.Parallel()
	for _, service := range []string{"systemd-logind", "systemd-machined"} {
		t.Run(service, func(t *testing.T) {
			t.Parallel()
			global := writeConfig(t, map[string]string{
				"sermo.yml": "engine: {backend: systemd}\npaths: {services: [@ROOT@/services]}\ndefaults: {policy: {cooldown: 5m}}\n",
				"services/manager.yml": "name: manager\nuses: " + service + `
rules:
  block-during-maintenance:
    type: guard
    blocks: [stop]
    if:
      command:
        command: [/fixture/maintenance-check]
        expect_exit: 0
        timeout: 5s
    then:
      action: block
      message: Maintenance in progress
`,
			})
			cfg, err := loadConfig(t, global, WithCatalogDirs(repoCatalogDir(repoRoot(t))))
			if err != nil {
				t.Fatal(err)
			}
			if issues := Validate(cfg); len(issues) != 0 {
				t.Fatal(issues)
			}
			resolved, errs := cfg.Resolve("manager")
			if len(errs) != 0 {
				t.Fatal(errs)
			}
			parsed, warnings := rules.ParseRules(resolved.Tree)
			if len(warnings) != 0 {
				t.Fatal(warnings)
			}
			runner := execxtest.Fixed(execx.Result{}, nil)
			ev := &rules.Evaluator{Deps: checks.Deps{Runner: runner, DefaultTimeout: time.Second}}
			name, reason, err := rules.Guard(t.Context(), parsed, string(rules.ActionRestart), ev)
			if err != nil || name != "block-during-maintenance" || reason != "Maintenance in progress" {
				t.Fatalf("operator stop guard must still protect restart: guard=%q reason=%q err=%v", name, reason, err)
			}
			calls := runner.Calls()
			if len(calls) != 1 || calls[0].Name != "/fixture/maintenance-check" || !runner.SawDeadline() {
				t.Fatalf("operator guard was not executed with a deadline: %v", calls)
			}
		})
	}
}
