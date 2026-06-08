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

func TestOSHookRunnerExecutesViaExecx(t *testing.T) {
	// Success path using real command (exercises OSHookRunner + execx.RunEnv + env merge)
	spec := HookSpec{Command: []string{"/bin/true"}, Timeout: time.Second}
	err := spec.Run(context.Background(), OSHookRunner{}, map[string]string{"SERMO_WATCH": "test-watch"})
	if err != nil {
		t.Fatalf("OSHookRunner with /bin/true failed: %v", err)
	}

	// Failure path (non-zero exit should surface as error)
	spec = HookSpec{Command: []string{"/bin/false"}, Timeout: time.Second}
	err = spec.Run(context.Background(), OSHookRunner{}, nil)
	if err == nil {
		t.Fatal("OSHookRunner with /bin/false should have returned error")
	}
}

func TestOSHookRunnerRespectsTimeout(t *testing.T) {
	// Command that takes too long should be killed by the timeout in RunHook / execx
	spec := HookSpec{Command: []string{"sleep", "2"}, Timeout: 50 * time.Millisecond}
	start := time.Now()
	err := spec.Run(context.Background(), OSHookRunner{}, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error from long-running hook")
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("hook did not respect timeout (took %v)", elapsed)
	}
}
