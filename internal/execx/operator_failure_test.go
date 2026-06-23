package execx

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOperatorFailureTimeout(t *testing.T) {
	err := errors.Join(errors.New("run salt-minion: timeout after 5.573s"), context.DeadlineExceeded)
	msg := OperatorFailure(err, Result{ExitCode: -1, Duration: 5573 * time.Millisecond}, 10*time.Second)
	if msg != "timeout after 10s" {
		t.Fatalf("OperatorFailure() = %q, want configured timeout", msg)
	}
}

func TestOperatorFailureTimeoutUsesDuration(t *testing.T) {
	err := context.DeadlineExceeded
	msg := OperatorFailure(err, Result{ExitCode: -1, Duration: 2 * time.Second}, 0)
	if msg != "timeout after 2s" {
		t.Fatalf("OperatorFailure() = %q, want duration fallback", msg)
	}
}

func TestContextFailureTimeout(t *testing.T) {
	msg := ContextFailure(context.DeadlineExceeded, 250*time.Millisecond)
	if msg != "timeout after 250ms" {
		t.Fatalf("ContextFailure() = %q, want timeout after 250ms", msg)
	}
}

func TestOperatorFailureStripsRunPrefix(t *testing.T) {
	err := errors.New("run nginx: exit code 1")
	msg := OperatorFailure(err, Result{ExitCode: 1}, time.Second)
	if msg != "exit code 1" {
		t.Fatalf("OperatorFailure() = %q, want stripped prefix", msg)
	}
}
