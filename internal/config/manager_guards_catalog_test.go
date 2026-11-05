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

func TestCatalogManagerGuardsProtectActiveWorkloads(t *testing.T) {
	for _, service := range []struct {
		name, guard, emptyList string
		args                   []string
	}{
		{"systemd-logind", "block-stop-during-login-sessions", "a(susso) 0", []string{"org.freedesktop.login1", "/org/freedesktop/login1", "org.freedesktop.login1.Manager", "ListSessions"}},
		{"systemd-machined", "block-stop-during-registered-machines", "a(ssso) 0", []string{"org.freedesktop.machine1", "/org/freedesktop/machine1", "org.freedesktop.machine1.Manager", "ListMachines"}},
	} {
		t.Run(service.name, func(t *testing.T) {
			resolved := resolveCatalogService(t, service.name, "systemd")
			parsed, warnings := rules.ParseRules(resolved.Tree)
			if len(warnings) != 0 {
				t.Fatal(warnings)
			}
			index := slices.IndexFunc(parsed, func(rule rules.Rule) bool { return rule.Name == service.guard })
			if index < 0 {
				t.Fatalf("missing workload guard %q", service.guard)
			}
			guards := parsed[index : index+1]
			for _, tc := range []struct {
				name   string
				result execx.Result
				err    error
				busy   bool
			}{
				{name: "empty-list", result: execx.Result{Stdout: service.emptyList}},
				{name: "empty-list-whitespace", result: execx.Result{Stdout: " " + service.emptyList + "\n\t\n"}},
				{name: "missing-response", busy: true},
				{name: "unknown-response", result: execx.Result{Stdout: "s \"unknown\""}, busy: true},
				{name: "active", result: execx.Result{Stdout: "fixture workload\n"}, busy: true},
				{name: "query-failed", result: execx.Result{ExitCode: 1}, busy: true},
				{name: "helper-missing", result: execx.Result{ExitCode: execx.ExitCodeRunFailure}, err: fs.ErrNotExist, busy: true},
				{name: "timeout", result: execx.Result{ExitCode: execx.ExitCodeRunFailure}, err: context.DeadlineExceeded, busy: true},
			} {
				for _, action := range []rules.ActionType{rules.ActionStop, rules.ActionRestart, rules.ActionStart} {
					t.Run(tc.name+"/"+string(action), func(t *testing.T) {
						runner := execxtest.Fixed(tc.result, tc.err)
						ev := &rules.Evaluator{Deps: checks.Deps{Runner: runner, DefaultTimeout: time.Second}}
						name, reason, err := rules.Guard(t.Context(), guards, string(action), ev)
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
						if len(calls) != 1 || calls[0].Name != "/usr/bin/busctl" || !slices.Equal(calls[0].Args, append([]string{"--system", "--auto-start=no", "--allow-interactive-authorization=no", "call"}, service.args...)) || !runner.SawDeadline() {
							t.Fatalf("workload inspection must use bounded native argv: calls=%v deadline=%v", calls, runner.SawDeadline())
						}
					})
				}
			}
		})
	}
}
