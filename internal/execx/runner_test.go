package execx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestCommandRunnerStdout(t *testing.T) {
	res, err := CommandRunner{}.Run(context.Background(), "sh", "-c", "printf hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Stdout != "hello" {
		t.Errorf("stdout = %q, want %q", res.Stdout, "hello")
	}
	if res.Stderr != "" {
		t.Errorf("stderr = %q, want empty", res.Stderr)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0", res.ExitCode)
	}
	if res.Duration <= 0 {
		t.Errorf("duration = %v, want > 0", res.Duration)
	}
}

func TestCommandRunnerStderr(t *testing.T) {
	res, err := CommandRunner{}.Run(context.Background(), "sh", "-c", "printf oops >&2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Stderr != "oops" {
		t.Errorf("stderr = %q, want %q", res.Stderr, "oops")
	}
}

func TestCommandRunnerExitCode(t *testing.T) {
	res, err := CommandRunner{}.Run(context.Background(), "sh", "-c", "exit 3")
	if err == nil {
		t.Fatal("expected an error for non-zero exit")
	}
	if res.ExitCode != 3 {
		t.Errorf("exit code = %d, want 3", res.ExitCode)
	}
	if !strings.Contains(err.Error(), "exit code 3") {
		t.Errorf("error = %q, want it to mention exit code 3", err)
	}
}

func TestCommandRunnerTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	res, err := CommandRunner{}.Run(ctx, "sleep", "5")
	if err == nil {
		t.Fatal("expected a timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error = %v, want context.DeadlineExceeded", err)
	}
	if res.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1 on timeout", res.ExitCode)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %v, expected the context to cancel it promptly", elapsed)
	}
}

func TestCommandRunnerNotFound(t *testing.T) {
	res, err := CommandRunner{}.Run(context.Background(), "sermo-no-such-command-xyz")
	if err == nil {
		t.Fatal("expected an error for a missing command")
	}
	if res.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1 when the command cannot start", res.ExitCode)
	}
}

func TestOSLookup(t *testing.T) {
	path, err := OSLookup{}.LookPath("sh")
	if err != nil {
		t.Fatalf("LookPath(sh): %v", err)
	}
	if path == "" {
		t.Error("LookPath(sh) returned an empty path")
	}

	if _, err := (OSLookup{}).LookPath("sermo-no-such-command-xyz"); err == nil {
		t.Error("expected LookPath to fail for a missing command")
	} else if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("error = %v, want it to wrap exec.ErrNotFound", err)
	}
}

func TestWithTimeout(t *testing.T) {
	t.Run("adds deadline when parent has none", func(t *testing.T) {
		ctx, cancel := WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		if _, ok := ctx.Deadline(); !ok {
			t.Fatal("expected deadline to be set")
		}
	})

	t.Run("zero timeout yields cancellable ctx without hard deadline", func(t *testing.T) {
		ctx, cancel := WithTimeout(context.Background(), 0)
		defer cancel()
		if _, ok := ctx.Deadline(); ok {
			t.Error("expected no deadline when timeout <= 0")
		}
	})

	t.Run("parent deadline is respected (earlier wins)", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
		defer cancel()
		child, cancel2 := WithTimeout(parent, 1*time.Hour)
		defer cancel2()

		dl, ok := child.Deadline()
		if !ok {
			t.Fatal("child should have a deadline")
		}
		if time.Until(dl) > 10*time.Millisecond {
			t.Errorf("child deadline too far in future; parent short deadline should win")
		}
	})
}

func TestPackageRun(t *testing.T) {
	t.Run("applies timeout when given", func(t *testing.T) {
		start := time.Now()
		_, err := Run(context.Background(), CommandRunner{}, 30*time.Millisecond, "sleep", "2")
		if err == nil {
			t.Fatal("expected timeout error")
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Errorf("took too long (%v); timeout was not effective", elapsed)
		}
	})

	t.Run("timeout=0 uses parent ctx as-is (no extra deadline added)", func(t *testing.T) {
		// We can't easily assert "no deadline was added" from outside without
		// inspecting the child, but we can at least prove it doesn't blow up
		// and that a very quick command succeeds.
		res, err := Run(context.Background(), CommandRunner{}, 0, "sh", "-c", "printf ok")
		if err != nil {
			t.Fatalf("unexpected error with timeout=0: %v", err)
		}
		if res.Stdout != "ok" {
			t.Errorf("stdout = %q, want ok", res.Stdout)
		}
	})

	t.Run("respects already-deadlined parent even with long timeout arg", func(t *testing.T) {
		parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		start := time.Now()
		_, err := Run(parent, CommandRunner{}, 5*time.Second, "sleep", "2")
		if err == nil {
			t.Fatal("expected deadline from parent to win")
		}
		if elapsed := time.Since(start); elapsed > 200*time.Millisecond {
			t.Errorf("parent deadline was not effective (took %v)", elapsed)
		}
	})
}

func TestCommandRunnerRunEnv(t *testing.T) {
	t.Run("custom env is passed to command", func(t *testing.T) {
		res, err := CommandRunner{}.RunEnv(context.Background(),
			[]string{"CUSTOM_VAR=from_test", "PATH=/bin"},
			"sh", "-c", "printf %s $CUSTOM_VAR")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Stdout != "from_test" {
			t.Errorf("stdout = %q, want from_test (custom env was not used)", res.Stdout)
		}
	})

	t.Run("nil env inherits process environment (like normal Run)", func(t *testing.T) {
		// We set a unique var in the test process and verify the child sees it.
		unique := "SERMO_TEST_HOOK_ENV_" + time.Now().Format("20060102150405")
		os.Setenv(unique, "inherited")
		defer os.Unsetenv(unique)

		res, err := CommandRunner{}.RunEnv(context.Background(), nil, "sh", "-c", "printf %s $"+unique)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if res.Stdout != "inherited" {
			t.Errorf("stdout = %q, want inherited (nil env should inherit)", res.Stdout)
		}
	})
}

func TestPackageRunEnv(t *testing.T) {
	t.Run("applies timeout and custom env", func(t *testing.T) {
		env := []string{"HOOK_VAR=hook_value"}
		start := time.Now()
		_, err := RunEnv(context.Background(), CommandRunner{}, env, 40*time.Millisecond, "sleep", "1")
		if err == nil {
			t.Fatal("expected timeout")
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Errorf("timeout was not effective")
		}
	})

	t.Run("rejects runners that do not implement EnvRunner", func(t *testing.T) {
		// A minimal runner that only implements Runner, not EnvRunner.
		basic := basicRunner{}
		_, err := RunEnv(context.Background(), basic, nil, 0, "echo", "hi")
		if err == nil || !strings.Contains(err.Error(), "does not support custom environment") {
			t.Errorf("expected error about missing EnvRunner support, got: %v", err)
		}
	})
}

// basicRunner is a test double that only satisfies Runner (not EnvRunner).
type basicRunner struct{}

func (basicRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return Result{}, nil
}
