package hostfs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var errTestHung = errors.New("test hung")

const inFlightBudget = 3 * time.Second

// waitIdle returns once key has no operation in flight: the goroutine that ran
// it removes the key as its last step, so no goroutine outlives the test.
func waitIdle(t *testing.T, g *InFlight[int], key string) {
	t.Helper()
	deadline := time.Now().Add(inFlightBudget)
	for {
		if _, err := g.Do(key, time.Second, func() (int, error) { return 0, nil }); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s still in flight", key)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestInFlightGivesUpAndRefusesUntilTheCallReturns(t *testing.T) {
	g := NewInFlight[int]("stat", errTestHung, 0)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		waitIdle(t, g, "/hung")
	})
	var calls atomic.Int32
	blocked := func() (int, error) {
		calls.Add(1)
		<-release
		return 1, nil
	}

	started := time.Now()
	_, err := g.Do("/hung", 10*time.Millisecond, blocked)
	if !errors.Is(err, errTestHung) || !strings.Contains(err.Error(), "no answer within 10ms") {
		t.Fatalf("first error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > inFlightBudget {
		t.Fatalf("gave up after %s", elapsed)
	}

	_, err = g.Do("/hung", time.Hour, blocked)
	if !errors.Is(err, errTestHung) || !strings.Contains(err.Error(), "an earlier stat is still blocked") {
		t.Fatalf("second error = %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("calls = %d, want no stacked call", got)
	}

	// Another key is not held back by the hung one.
	if got, err := g.Do("/other", time.Second, func() (int, error) { return 2, nil }); got != 2 || err != nil {
		t.Fatalf("other key = %d, %v", got, err)
	}

	unblock()
	waitIdle(t, g, "/hung")
	if got, err := g.Do("/hung", time.Second, func() (int, error) { return 3, nil }); got != 3 || err != nil {
		t.Fatalf("after the call returned = %d, %v; want a fresh run, not the dropped result", got, err)
	}
}

// A second caller waits on the running call instead of starting its own, and
// its shorter patience does not take the answer from the first.
func TestInFlightSharesARunningCall(t *testing.T) {
	g := NewInFlight[int]("stat", errTestHung, 0)
	entered, release := make(chan struct{}), make(chan struct{})
	first := make(chan int, 1)
	go func() {
		value, err := g.Do("/path", inFlightBudget, func() (int, error) {
			close(entered)
			<-release
			return 7, nil
		})
		if err != nil {
			t.Errorf("first caller: %v", err)
		}
		first <- value
	}()
	<-entered
	_, err := g.Do("/path", time.Millisecond, func() (int, error) {
		t.Error("the second caller started its own run")
		return 0, nil
	})
	if !errors.Is(err, errTestHung) {
		t.Fatalf("second caller = %v, want it to have waited on the running call", err)
	}
	close(release)
	if value := <-first; value != 7 {
		t.Fatalf("first caller = %d, want the run's answer", value)
	}
}

// A run that honors its own deadline answers inside the grace, so it is not
// called hung; the error still reports the timeout the operator configured.
func TestInFlightGraceOutlastsTheTimeoutItReports(t *testing.T) {
	g := NewInFlight[int]("read", errTestHung, inFlightBudget)
	got, err := g.Do("/slow", time.Millisecond, func() (int, error) {
		time.Sleep(5 * time.Millisecond)
		return 4, nil
	})
	if got != 4 || err != nil {
		t.Fatalf("Do = %d, %v; want the slow run's answer", got, err)
	}

	short := NewInFlight[int]("read", errTestHung, time.Millisecond)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		waitIdle(t, short, "/hung")
	})
	_, err = short.Do("/hung", time.Millisecond, func() (int, error) { <-release; return 0, nil })
	if err == nil || !strings.Contains(err.Error(), "no answer within 1ms") {
		t.Fatalf("error = %v, want the configured timeout, not timeout plus grace", err)
	}
}

// Steps prove a run alive: only silence is a hang, however long the whole takes.
func TestInFlightStepsExtendTheWait(t *testing.T) {
	const timeout = 150 * time.Millisecond
	g := NewInFlight[int]("scan", errTestHung, 0)
	got, err := g.DoSteps(t.Context(), "/tree", timeout, func(step func()) (int, error) {
		for range 6 {
			time.Sleep(timeout / 4)
			step()
		}
		return 5, nil
	})
	if got != 5 || err != nil {
		t.Fatalf("DoSteps = %d, %v; want the run's answer past its timeout", got, err)
	}
}

func TestInFlightGivesUpOnSilenceAfterSteps(t *testing.T) {
	g := NewInFlight[int]("scan", errTestHung, 0)
	release := make(chan struct{})
	t.Cleanup(func() {
		close(release)
		waitIdle(t, g, "/tree")
	})
	_, err := g.DoSteps(t.Context(), "/tree", 10*time.Millisecond, func(step func()) (int, error) {
		step()
		<-release
		return 0, nil
	})
	if !errors.Is(err, errTestHung) {
		t.Fatalf("error = %v, want a run gone silent called hung", err)
	}
}

// A caller that is cancelled leaves a run that is only slow: it is not blocked,
// so the next caller waits on it instead of being refused or starting another.
func TestInFlightCancelledCallerLeavesTheRunToTheNext(t *testing.T) {
	g := NewInFlight[int]("scan", errTestHung, 0)
	entered, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		close(release)
		waitIdle(t, g, "/tree")
	})
	ctx, cancel := context.WithCancel(t.Context())
	left := make(chan error, 1)
	go func() {
		_, err := g.DoSteps(ctx, "/tree", time.Hour, func(func()) (int, error) {
			close(entered)
			<-release
			return 0, nil
		})
		left <- err
	}()
	<-entered
	cancel()
	if err := <-left; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled caller = %v", err)
	}
	_, err := g.Do("/tree", time.Millisecond, func() (int, error) {
		t.Error("the next caller started its own run")
		return 0, nil
	})
	if err == nil || !strings.Contains(err.Error(), "no answer within") {
		t.Fatalf("next caller = %v, want it to have waited on the running call", err)
	}
}

func TestInFlightRunsInlineWithoutATimeout(t *testing.T) {
	g := NewInFlight[int]("stat", errTestHung, 0)
	failure := errors.New("boom")
	if _, err := g.Do("/path", 0, func() (int, error) { return 0, failure }); !errors.Is(err, failure) {
		t.Fatalf("error = %v", err)
	}
	if len(g.calls) != 0 {
		t.Fatalf("calls = %v, want nothing registered", g.calls)
	}
}
