package execx

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
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
	if !strings.Contains(err.Error(), "timeout after") {
		t.Errorf("error = %q, want timeout after duration", err.Error())
	}
	if res.ExitCode != -1 {
		t.Errorf("exit code = %d, want -1 on timeout", res.ExitCode)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %v, expected the context to cancel it promptly", elapsed)
	}
}

func TestCommandRunnerTimeoutKillsProcessGroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("process-group cancellation is linux-specific")
	}
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	env := []string{
		"PATH=/bin:/usr/bin",
		"SERMO_CHILD_PID=" + pidFile,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := CommandRunner{}.RunEnv(ctx, env, "sh", "-c", `sleep 5 & printf '%s' "$!" > "$SERMO_CHILD_PID"; wait`)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("took %v; child process kept command pipes open after timeout", elapsed)
	}

	data, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read child pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatalf("parse child pid %q: %v", data, err)
	}
	if processStillExists(pid) {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("child process %d survived command timeout", pid)
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

func processStillExists(pid int) bool {
	for range 20 {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
	return true
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

func TestPackageRunUser(t *testing.T) {
	t.Run("rejects runners that do not implement UserRunner", func(t *testing.T) {
		res, err := RunUser(context.Background(), basicRunner{}, 0, "postgres", "echo", "hi")
		if err == nil || !strings.Contains(err.Error(), "does not support user") {
			t.Fatalf("expected missing UserRunner support, got: %v", err)
		}
		if res.ExitCode != -1 {
			t.Fatalf("reject exit code = %d, want -1 (run failure, not a real exit)", res.ExitCode)
		}
	})

	t.Run("unknown user fails before command starts", func(t *testing.T) {
		res, err := RunUser(context.Background(), CommandRunner{}, time.Second, "sermo-no-such-user-xyz", "sh", "-c", "exit 0")
		if err == nil {
			t.Fatal("expected unknown user error")
		}
		if res.ExitCode != -1 {
			t.Fatalf("exit code = %d, want -1", res.ExitCode)
		}
		if !strings.Contains(err.Error(), "command user") {
			t.Fatalf("error = %q, want command user detail", err)
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

	t.Run("success path with timeout>0 and timeout=0 for quick command", func(t *testing.T) {
		env := []string{"REVIEW_VAR=from_review_test"}
		// timeout=0: parent as-is
		res, err := RunEnv(context.Background(), CommandRunner{}, env, 0, "sh", "-c", "printf $REVIEW_VAR")
		if err != nil {
			t.Fatalf("RunEnv timeout=0 quick: %v", err)
		}
		if res.Stdout != "from_review_test" {
			t.Errorf("stdout = %q, want from_review_test", res.Stdout)
		}
		// positive timeout that succeeds
		res, err = RunEnv(context.Background(), CommandRunner{}, env, 2*time.Second, "sh", "-c", "printf ok")
		if err != nil {
			t.Fatalf("RunEnv positive-timeout quick: %v", err)
		}
		if res.Stdout != "ok" {
			t.Errorf("stdout = %q, want ok", res.Stdout)
		}
	})
}

// basicRunner is a test double that only satisfies Runner (not EnvRunner).
type basicRunner struct{}

func (basicRunner) Run(ctx context.Context, name string, args ...string) (Result, error) {
	return Result{}, nil
}

func TestOperatorFailureUsesMeasuredDurationWhenNoTimeout(t *testing.T) {
	// With no explicit timeout, a deadline-exceeded error falls back to the
	// measured run duration for the operator message.
	msg := OperatorFailure(context.DeadlineExceeded, Result{Duration: 5 * time.Millisecond}, 0)
	if !strings.Contains(msg, "timeout after 5ms") {
		t.Fatalf("OperatorFailure = %q, want it to mention the 5ms measured duration", msg)
	}
}
