package config

import (
	"context"
	"io/fs"
	"slices"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/rules"
)

func TestCatalogLogindGuardsLocalSessions(t *testing.T) {
	t.Parallel()
	resolved := resolveCatalogService(t, "systemd-logind", "systemd")
	parsed, warnings := rules.ParseRules(resolved.Tree)
	if len(warnings) != 0 {
		t.Fatal(warnings)
	}
	const remote = `["c1",1000,"alice","","/org/freedesktop/login1/session/c1"]`
	const background = `["c2",1000,"alice","","/org/freedesktop/login1/session/c2"]`
	const local = `["c3",1001,"bob","seat0","/org/freedesktop/login1/session/c3"]`
	report := func(rows string) execx.Result {
		return execx.Result{Stdout: `{"type":"a(susso)","data":[[` + rows + `]]}`}
	}
	for _, tc := range []struct {
		name   string
		result execx.Result
		err    error
		busy   bool
	}{
		{name: "empty", result: report("")},
		{name: "SSH", result: report(remote)},
		{name: "SSH and background", result: report(remote + "," + background)},
		{name: "escaped account", result: report(`["c1",1000,"domain\\alice","","/org/freedesktop/login1/session/c1"]`)},
		{name: "outer whitespace", result: execx.Result{Stdout: " \n" + report(remote).Stdout + "\n\t"}},
		{name: "local seat", result: report(local), busy: true},
		{name: "local after SSH", result: report(remote + "," + local), busy: true},
		{name: "local before SSH", result: report(local + "," + remote), busy: true},
		{name: "null seat", result: report(`["c1",1000,"alice",null,"/org/freedesktop/login1/session/c1"]`), busy: true},
		{name: "missing seat", result: report(`["c1",1000,"alice","/org/freedesktop/login1/session/c1"]`), busy: true},
		{name: "truncated response", result: execx.Result{Stdout: `{"type":"a(susso)","data":[[` + remote}, busy: true},
		{name: "trailing comma", result: report(remote + ","), busy: true},
		{name: "missing separator", result: report(remote + background), busy: true},
		{name: "missing response", busy: true},
		{name: "unknown response", result: execx.Result{Stdout: `{"type":"s","data":[]}`}, busy: true},
		{name: "query failed", result: execx.Result{ExitCode: 1}, busy: true},
		{name: "helper missing", result: execx.Result{ExitCode: execx.ExitCodeRunFailure}, err: fs.ErrNotExist, busy: true},
		{name: "timeout", result: execx.Result{ExitCode: execx.ExitCodeRunFailure}, err: context.DeadlineExceeded, busy: true},
	} {
		for _, action := range []rules.ActionType{rules.ActionStop, rules.ActionRestart, rules.ActionStart} {
			t.Run(tc.name+"/"+string(action), func(t *testing.T) {
				t.Parallel()
				runner := execxtest.Fixed(tc.result, tc.err)
				ev := &rules.Evaluator{Deps: checks.Deps{Runner: runner, DefaultTimeout: time.Second}}
				name, reason, err := rules.Guard(t.Context(), parsed, string(action), ev)
				wantBlocked := tc.busy && action != rules.ActionStart
				if blocked := name != "" || err != nil; blocked != wantBlocked {
					t.Fatalf("blocked=%v, want %v: guard=%q reason=%q err=%v", blocked, wantBlocked, name, reason, err)
				}
				calls := runner.Calls()
				if action == rules.ActionStart {
					if len(calls) != 0 {
						t.Fatalf("startup must remain available: calls=%v", calls)
					}
					return
				}
				args := []string{"--system", "--auto-start=no", "--allow-interactive-authorization=no", "--json=short", "call", "org.freedesktop.login1", "/org/freedesktop/login1", "org.freedesktop.login1.Manager", "ListSessions"}
				if len(calls) != 1 || calls[0].Name != "/usr/bin/busctl" || !slices.Equal(calls[0].Args, args) || !runner.SawDeadline() {
					t.Fatalf("session inspection must use bounded native argv: calls=%v deadline=%v", calls, runner.SawDeadline())
				}
			})
		}
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
