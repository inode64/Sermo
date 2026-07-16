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

// With the ": " separator at the very start of the post-"run " remainder, the
// strip still applies (the index-zero boundary of the prefix search).
func TestOperatorFailureStripsRunPrefixAtStart(t *testing.T) {
	msg := OperatorFailure(errors.New("run : detail"), Result{ExitCode: 1}, time.Second)
	if msg != "detail" {
		t.Fatalf("OperatorFailure(run : detail) = %q, want \"detail\"", msg)
	}
}

// A timeout with neither a configured timeout nor a measured duration falls
// through to a bare "timeout" (the d>0 boundary at d==0).
func TestOperatorFailureTimeoutNoDuration(t *testing.T) {
	if msg := OperatorFailure(context.DeadlineExceeded, Result{}, 0); msg != "timeout" {
		t.Fatalf("OperatorFailure(deadline, {}, 0) = %q, want \"timeout\"", msg)
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

func TestOperatorFailureEmptyErrorUsesCallerFallback(t *testing.T) {
	if msg := OperatorFailure(nil, Result{ExitCode: -1}, time.Second); msg != "" {
		t.Fatalf("OperatorFailure(nil) = %q, want empty for caller fallback", msg)
	}
}

func TestOperatorFailureOr(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		fallback string
		want     string
	}{
		{name: "uses failure", err: errors.New("run smartctl: unavailable"), fallback: CommandDidNotStart, want: "unavailable"},
		{name: "uses fallback", fallback: CommandDidNotStart, want: CommandDidNotStart},
		{name: "preserves timeout", err: context.DeadlineExceeded, fallback: CommandDidNotStart, want: "timeout after 1s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := OperatorFailureOr(tt.err, Result{}, time.Second, tt.fallback); got != tt.want {
				t.Fatalf("OperatorFailureOr() = %q, want %q", got, tt.want)
			}
		})
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
