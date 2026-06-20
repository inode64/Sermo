package app

import (
	"context"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/execx"
)

// stubHookRunner returns a fixed result/error, for exercising HookSpec.Run's
// exit-code and stdout/stderr assertions without spawning a process.
type stubHookRunner struct {
	res execx.Result
	err error
}

func (s stubHookRunner) RunHook(context.Context, []string, map[string]string, time.Duration) (execx.Result, error) {
	return s.res, s.err
}

func TestHookSpecRunExpectations(t *testing.T) {
	cases := []struct {
		name    string
		spec    HookSpec
		res     execx.Result
		err     error
		wantErr bool
	}{
		{"default exit 0 ok", HookSpec{Command: []string{"x"}}, execx.Result{ExitCode: 0}, nil, false},
		{"default exit nonzero fails", HookSpec{Command: []string{"x"}}, execx.Result{ExitCode: 1, Stderr: "boom\n"}, nil, true},
		{"expect_exit matches nonzero", HookSpec{Command: []string{"x"}, ExpectExit: []int{2}}, execx.Result{ExitCode: 2}, nil, false},
		{"expect_exit matches one of several exits", HookSpec{Command: []string{"x"}, ExpectExit: []int{0, 2}}, execx.Result{ExitCode: 2}, nil, false},
		{"expect_exit mismatch", HookSpec{Command: []string{"x"}, ExpectExit: []int{2}}, execx.Result{ExitCode: 0}, nil, true},
		{"stdout substring ok", HookSpec{Command: []string{"x"}, Stdout: checks.OutputMatcher{Substring: "done"}}, execx.Result{Stdout: "all done\n"}, nil, false},
		{"stdout substring missing", HookSpec{Command: []string{"x"}, Stdout: checks.OutputMatcher{Substring: "done"}}, execx.Result{Stdout: "nope\n"}, nil, true},
		{"stderr op ok", HookSpec{Command: []string{"x"}, Stderr: checks.OutputMatcher{Op: "==", Value: ""}}, execx.Result{Stderr: ""}, nil, false},
		{"stderr op fail", HookSpec{Command: []string{"x"}, Stderr: checks.OutputMatcher{Op: "==", Value: ""}}, execx.Result{Stderr: "warn\n"}, nil, true},
		{"runner error is fatal", HookSpec{Command: []string{"x"}}, execx.Result{ExitCode: -1}, context.DeadlineExceeded, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.spec.Run(context.Background(), stubHookRunner{res: c.res, err: c.err}, nil)
			if (err != nil) != c.wantErr {
				t.Fatalf("Run() err = %v, wantErr %v", err, c.wantErr)
			}
		})
	}
}

func TestHookRunnerPassesArgvEnvTimeout(t *testing.T) {
	var gotArgv []string
	var gotEnv map[string]string
	var gotTimeout time.Duration
	runner := HookRunnerFunc(func(_ context.Context, argv []string, env map[string]string, timeout time.Duration) error {
		gotArgv, gotEnv, gotTimeout = argv, env, timeout
		return nil
	})

	spec := HookSpec{Command: []string{"/bin/echo", "hi"}, Timeout: 5 * time.Second}
	err := spec.Run(context.Background(), runner, map[string]string{"SERMO_WATCH": "storage-root"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gotArgv) != 2 || gotArgv[0] != "/bin/echo" {
		t.Fatalf("argv = %v", gotArgv)
	}
	if gotEnv["SERMO_WATCH"] != "storage-root" {
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

// fakeEnvRunner is a test double for execx that records RunEnv calls (for env/argv/timeout verification)
// without performing real execution. It implements execx.EnvRunner.
type fakeEnvRunner struct {
	calls []struct {
		env  []string
		name string
		args []string
	}
	result execx.Result
	err    error
}

func (f *fakeEnvRunner) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return f.result, f.err
}

func (f *fakeEnvRunner) RunEnv(ctx context.Context, env []string, name string, args ...string) (execx.Result, error) {
	f.calls = append(f.calls, struct {
		env  []string
		name string
		args []string
	}{env, name, args})
	return f.result, f.err
}

func TestOSHookRunnerWithInjectedExecxRunner(t *testing.T) {
	fake := &fakeEnvRunner{
		result: execx.Result{ExitCode: 0},
	}
	spec := HookSpec{Command: []string{"/bin/echo", "hello"}, Timeout: 123 * time.Second}
	injectedEnv := map[string]string{
		"SERMO_WATCH": "my-watch",
		"SERMO_FOO":   "bar",
	}
	err := spec.Run(context.Background(), OSHookRunner{Runner: fake}, injectedEnv)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("expected 1 call to execx, got %d", len(fake.calls))
	}
	call := fake.calls[0]
	if call.name != "/bin/echo" || len(call.args) != 1 || call.args[0] != "hello" {
		t.Fatalf("bad argv passed to execx: name=%s args=%v", call.name, call.args)
	}
	// Verify that the full env passed to execx contains both os.Environ() base + injected SERMO_ vars
	hasWatch := false
	hasFoo := false
	for _, e := range call.env {
		if e == "SERMO_WATCH=my-watch" {
			hasWatch = true
		}
		if e == "SERMO_FOO=bar" {
			hasFoo = true
		}
	}
	if !hasWatch || !hasFoo {
		t.Fatalf("injected SERMO_ vars not found in env passed to execx; env had %d entries, sample: %v", len(call.env), call.env[:min(5, len(call.env))])
	}
}
