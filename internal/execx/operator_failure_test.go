package execx

import (
	"context"
	"errors"
	"fmt"
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

func TestContextFailureCanceled(t *testing.T) {
	msg := ContextFailure(context.Canceled, time.Second)
	if msg != "cancelled" {
		t.Fatalf("ContextFailure() = %q, want cancelled", msg)
	}
}

func TestOperatorFailureCanceledStripsRunPrefix(t *testing.T) {
	err := fmt.Errorf("run hdparm: %w", context.Canceled)
	msg := OperatorFailure(err, Result{ExitCode: -1}, time.Second)
	if msg != "cancelled" {
		t.Fatalf("OperatorFailure() = %q, want cancelled", msg)
	}
}

func TestFormatContextOrError(t *testing.T) {
	if got := FormatContextOrError(context.DeadlineExceeded, 500*time.Millisecond); got != "timeout after 500ms" {
		t.Fatalf("FormatContextOrError(deadline) = %q", got)
	}
	if got := FormatContextOrError(errors.New("mount busy"), time.Second); got != "mount busy" {
		t.Fatalf("FormatContextOrError(other) = %q", got)
	}
}
