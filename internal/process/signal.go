package process

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"syscall"
	"time"
)

// Signaler delivers a signal to a process. It is an interface so escalation can
// be tested without touching real processes.
type Signaler interface {
	Signal(pid int, sig syscall.Signal) error
}

// OSSignaler sends real signals via kill(2).
type OSSignaler struct{}

// Signal delivers sig to pid, refusing non-positive PIDs (which have special
// process-group semantics and must never be signalled here).
func (OSSignaler) Signal(pid int, sig syscall.Signal) error {
	if pid <= 0 {
		return fmt.Errorf("refusing to signal pid %d", pid)
	}
	return syscall.Kill(pid, sig)
}

// ReapResult is the outcome of signal escalation.
type ReapResult struct {
	Remaining []Process // residuals still present at the end (orphans)
	Signalled []int     // pids that were signalled, sorted
}

// OK reports whether no residual processes remain (section 22: ok only if none
// remain at all).
func (r ReapResult) OK() bool { return len(r.Remaining) == 0 }

// Reaper applies the stop/kill signal escalation policy (section 22) to residual
// processes. Rediscover re-reads the current residual set between steps so
// escalation re-evaluates identity each round (defending against PID reuse).
type Reaper struct {
	Rediscover  func() []Process
	Signaler    Signaler
	ResolveUser UserResolver
	Sleep       func(time.Duration)
}

// Reap escalates signals against residuals according to policy (section 22):
//
//   - no residuals          -> ok.
//   - force_kill=false       -> signal nothing; residuals are orphans.
//   - force_kill=true        -> SIGTERM the killable set, wait term_timeout,
//     rediscover; SIGKILL survivors, wait kill_timeout, rediscover; whatever
//     remains is an orphan.
//
// Only processes that exactly match kill_only_if are ever signalled; a residual
// that does not match is reported, never touched.
func (r Reaper) Reap(ctx context.Context, residuals []Process, policy KillPolicy) ReapResult {
	if len(residuals) == 0 {
		return ReapResult{}
	}
	if !policy.ForceKill {
		return ReapResult{Remaining: residuals}
	}

	resolve := r.ResolveUser
	if resolve == nil {
		resolve = OSUserResolver
	}
	sleep := r.Sleep
	if sleep == nil {
		sleep = time.Sleep
	}
	signaler := r.Signaler
	if signaler == nil {
		signaler = OSSignaler{}
	}
	if r.Rediscover == nil {
		// Without a way to verify survivors, treat residuals as orphans rather
		// than claim success.
		return ReapResult{Remaining: residuals}
	}

	signalled := map[int]bool{}
	round := func(set []Process, sig syscall.Signal) {
		for _, p := range set {
			if policy.KillOnlyIf.Killable(p, resolve) {
				if err := signaler.Signal(p.PID, sig); err == nil {
					signalled[p.PID] = true
				}
			}
		}
	}

	round(residuals, syscall.SIGTERM)
	if err := Wait(ctx, sleep, policy.TermTimeout); err != nil {
		return ReapResult{Remaining: r.Rediscover(), Signalled: sortedInts(signalled)}
	}
	residuals = r.Rediscover()
	if len(residuals) == 0 {
		return ReapResult{Signalled: sortedInts(signalled)}
	}

	round(residuals, syscall.SIGKILL)
	if err := Wait(ctx, sleep, policy.KillTimeout); err != nil {
		return ReapResult{Remaining: r.Rediscover(), Signalled: sortedInts(signalled)}
	}
	residuals = r.Rediscover()

	return ReapResult{Remaining: residuals, Signalled: sortedInts(signalled)}
}

// Wait blocks for d, returning early if ctx is cancelled. A non-positive d is an
// immediate ctx-check. sleep is injectable for tests (defaults to time.Sleep). It
// is the shared cancellable-sleep used by the reaper and the operation engine.
func Wait(ctx context.Context, sleep func(time.Duration), d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if sleep == nil {
		sleep = time.Sleep
	}
	done := make(chan struct{})
	go func() {
		sleep(d)
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return ctx.Err()
	}
}

func sortedInts(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for pid := range set {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}

// signalNames maps the signal names accepted in configuration to their numbers.
// These are the signals daemons actually use to reload/rotate/cycle in place; the
// fatal/stop signals (TERM/KILL) are handled by the stop policy, not reload.
var signalNames = map[string]syscall.Signal{
	"HUP":   syscall.SIGHUP,
	"INT":   syscall.SIGINT,
	"QUIT":  syscall.SIGQUIT,
	"USR1":  syscall.SIGUSR1,
	"USR2":  syscall.SIGUSR2,
	"TERM":  syscall.SIGTERM,
	"WINCH": syscall.SIGWINCH,
	"CONT":  syscall.SIGCONT,
}

// ParseSignal resolves a configured signal name to its syscall.Signal. The name
// is case-insensitive and an optional `SIG` prefix is accepted (`HUP`, `sighup`,
// `SIGHUP` all resolve to SIGHUP). An unknown name is an error so a typo in a
// `reload.signal` fails validation instead of silently sending nothing.
func ParseSignal(name string) (syscall.Signal, error) {
	key := strings.ToUpper(strings.TrimSpace(name))
	key = strings.TrimPrefix(key, "SIG")
	if sig, ok := signalNames[key]; ok {
		return sig, nil
	}
	return 0, fmt.Errorf("unknown signal %q", name)
}

// SignalNames returns the accepted signal names, sorted, for diagnostics and docs.
func SignalNames() []string {
	out := make([]string, 0, len(signalNames))
	for name := range signalNames {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
