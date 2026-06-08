package execx

import (
	"context"
	"errors"
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
