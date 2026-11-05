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
)

func TestMonitCatalogUsesNativeReadOnlyControlQuery(t *testing.T) {
	resolved := resolveCatalogService(t, "monit", "systemd")
	section := resolved.Tree["checks"].(map[string]any)
	port := section["port"].(map[string]any)
	for _, tc := range []struct {
		name   string
		result execx.Result
		err    error
		ok     bool
	}{
		{name: "control-interface-answers", result: execx.Result{Stdout: "Monit fixture\nprocess Running\n"}, ok: true},
		{name: "supervised-service-unhealthy", result: execx.Result{Stdout: "Monit fixture\nprocess Does not exist\n"}, ok: true},
		{name: "interface-disabled", result: execx.Result{ExitCode: 1}},
		{name: "helper-missing", result: execx.Result{ExitCode: execx.ExitCodeRunFailure}, err: fs.ErrNotExist},
		{name: "timeout", result: execx.Result{ExitCode: execx.ExitCodeRunFailure}, err: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := execxtest.Fixed(tc.result, tc.err)
			built, issues := checks.Build(map[string]any{"port": port}, checks.Deps{Service: "monit", Runner: runner, DefaultTimeout: time.Second})
			if len(issues) != 0 || len(built) != 1 {
				t.Fatalf("native control check: built=%v issues=%v", built, issues)
			}
			results := checks.Run(t.Context(), built, 1)
			if len(results) != 1 || results[0].OK != tc.ok || !results[0].Optional {
				t.Fatalf("control result=%+v, want ok=%v optional=true", results, tc.ok)
			}
			calls := runner.Calls()
			if len(calls) != 1 || calls[0].Name != "/usr/bin/monit" || !slices.Equal(calls[0].Args, []string{"-B", "-c", "/etc/monitrc", "summary"}) || !runner.SawDeadline() {
				t.Fatalf("control query must use bounded native argv: calls=%v deadline=%v", calls, runner.SawDeadline())
			}
		})
	}
}
