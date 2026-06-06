package app

import (
	"context"
	"testing"
	"time"
)

func TestHookRunnerPassesArgvEnvTimeout(t *testing.T) {
	var gotArgv []string
	var gotEnv map[string]string
	var gotTimeout time.Duration
	runner := HookRunnerFunc(func(_ context.Context, argv []string, env map[string]string, timeout time.Duration) error {
		gotArgv, gotEnv, gotTimeout = argv, env, timeout
		return nil
	})

	spec := HookSpec{Command: []string{"/bin/echo", "hi"}, Timeout: 5 * time.Second}
	err := spec.Run(context.Background(), runner, map[string]string{"SERMO_WATCH": "disk-root"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotArgv) != 2 || gotArgv[0] != "/bin/echo" {
		t.Fatalf("argv = %v", gotArgv)
	}
	if gotEnv["SERMO_WATCH"] != "disk-root" {
		t.Fatalf("env = %v", gotEnv)
	}
	if gotTimeout != 5*time.Second {
		t.Fatalf("timeout = %v", gotTimeout)
	}
}

func TestHookRunnerRejectsEmptyCommand(t *testing.T) {
	spec := HookSpec{}
	err := spec.Run(context.Background(), HookRunnerFunc(func(context.Context, []string, map[string]string, time.Duration) error { return nil }), nil)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}
