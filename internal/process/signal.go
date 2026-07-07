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
	if err := protectedSignalTarget(pid, sig, OSReader{}.Identity); err != nil {
		return err
	}
	return syscall.Kill(pid, sig)
}

// ReapResult is the outcome of signal escalation.
type ReapResult struct {
	Remaining []Process       // residuals still present at the end (orphans)
	Signalled []int           // pids that were signalled, sorted
	Failed    []SignalFailure // signal delivery failures, sorted by PID
}

// OK reports whether no residual processes remain (ok only if none
// remain at all).
func (r ReapResult) OK() bool { return len(r.Remaining) == 0 }

// SignalFailure records a signal delivery error for a PID.
type SignalFailure struct {
	PID    int            // process ID that could not be signalled
	Signal syscall.Signal // signal attempted
	Err    error          // delivery error
}

// Reaper applies the stop/kill signal escalation policy to residual
// processes. Rediscover re-reads the current residual set between steps so
// escalation re-evaluates identity each round (defending against PID reuse).
type Reaper struct {
	Rediscover  func() []Process
	Signaler    Signaler
	ResolveUser UserResolver
	Sleep       func(time.Duration)
}

// Reap escalates signals against residuals according to policy:
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
		resolve = DefaultUserLookup().ResolveUser
	}
	// Leave sleep nil when unset so Wait uses its cancellable timer in production;
	// tests inject a fake to control timing. (Reap uses sleep only for Wait.)
	sleep := r.Sleep
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
	var failed []SignalFailure
	round := func(set []Process, sig syscall.Signal) {
		failed = append(failed, signalRound(set, policy.KillOnlyIf, resolve, signaler, sig, signalled)...)
	}

	round(residuals, syscall.SIGTERM)
	if err := Wait(ctx, sleep, policy.TermTimeout); err != nil {
		return ReapResult{Remaining: r.Rediscover(), Signalled: sortedInts(signalled), Failed: failed}
	}
	residuals = r.Rediscover()
	if len(residuals) == 0 {
		return ReapResult{Signalled: sortedInts(signalled), Failed: failed}
	}

	round(residuals, syscall.SIGKILL)
	if err := Wait(ctx, sleep, policy.KillTimeout); err != nil {
		return ReapResult{Remaining: r.Rediscover(), Signalled: sortedInts(signalled), Failed: failed}
	}
	residuals = r.Rediscover()

	return ReapResult{Remaining: residuals, Signalled: sortedInts(signalled), Failed: failed}
}

// Signal sends one signal to the processes allowed by selector. It shares the
// same identity gate as Reap, without applying the stop-policy TERM->KILL flow.
func (r Reaper) Signal(ctx context.Context, procs []Process, selector KillSelector, sig syscall.Signal) ReapResult {
	if len(procs) == 0 {
		return ReapResult{}
	}
	if err := ctx.Err(); err != nil {
		return ReapResult{Remaining: procs}
	}
	resolve := r.ResolveUser
	if resolve == nil {
		resolve = DefaultUserLookup().ResolveUser
	}
	signaler := r.Signaler
	if signaler == nil {
		signaler = OSSignaler{}
	}
	signalled := map[int]bool{}
	failed := signalRound(procs, selector, resolve, signaler, sig, signalled)
	remaining := procs
	if r.Rediscover != nil {
		remaining = r.Rediscover()
	}
	return ReapResult{Remaining: remaining, Signalled: sortedInts(signalled), Failed: failed}
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
		// Default: a stoppable timer so a cancelled Wait leaks no goroutine. An
		// injected sleep (tests) takes the goroutine path below, where the fake
		// returns promptly and so cannot leak — unlike a real time.Sleep, which
		// is not cancellable and would block until d elapsed.
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return ctx.Err()
		}
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

func signalRound(set []Process, selector KillSelector, resolve UserResolver, signaler Signaler, sig syscall.Signal, signalled map[int]bool) []SignalFailure {
	var failed []SignalFailure
	for _, p := range set {
		if selector.Killable(p, resolve) {
			if err := signaler.Signal(p.PID, sig); err == nil {
				signalled[p.PID] = true
			} else {
				failed = append(failed, SignalFailure{PID: p.PID, Signal: sig, Err: err})
			}
		}
	}
	sort.Slice(failed, func(i, j int) bool { return failed[i].PID < failed[j].PID })
	return failed
}

type signalTargetProbe func(int) (Identity, bool)

func protectedSignalTarget(pid int, sig syscall.Signal, probe signalTargetProbe) error {
	if !terminatingSignal(sig) {
		return nil
	}
	if pid == 1 {
		return fmt.Errorf("refusing to send %s to protected pid 1", sigName(sig))
	}
	id, ok := probe(pid)
	if pid == 2 && !ok {
		return fmt.Errorf("refusing to send %s to protected kernel pid 2", sigName(sig))
	}
	if ok && protectedKernelProcess(id.PID, id.PPID, id.ExeOK, id.Cmdline) {
		return fmt.Errorf("refusing to send %s to protected kernel process pid %d", sigName(sig), pid)
	}
	return nil
}

func terminatingSignal(sig syscall.Signal) bool {
	switch sig {
	case syscall.SIGKILL, syscall.SIGTERM, syscall.SIGINT, syscall.SIGQUIT:
		return true
	default:
		return false
	}
}

func sigName(sig syscall.Signal) string {
	switch sig {
	case syscall.SIGKILL:
		return "SIGKILL"
	case syscall.SIGTERM:
		return "SIGTERM"
	case syscall.SIGINT:
		return "SIGINT"
	case syscall.SIGQUIT:
		return "SIGQUIT"
	case syscall.SIGHUP:
		return "SIGHUP"
	default:
		return sig.String()
	}
}

// signalNames maps the signal names accepted in configuration to their numbers.
// These are the signals daemons actually use to reload/rotate/cycle in place; the
// fatal/stop signals (TERM/KILL) are handled by the stop policy, not reload.
const signalNamePrefix = "SIG"

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
	key = strings.TrimPrefix(key, signalNamePrefix)
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

// killSignalNames are the termination signals a kill action may send. KILL is
// intentionally absent from signalNames (which lists in-place reload signals);
// it is valid only where the intent is to terminate a process, such as a host
// process watch's `then.kill` action.
var killSignalNames = map[string]syscall.Signal{
	"TERM": syscall.SIGTERM,
	"KILL": syscall.SIGKILL,
}

// ParseKillSignal resolves a termination signal name for a kill action. The name
// is case-insensitive and an optional `SIG` prefix is accepted (`TERM`, `sigterm`,
// `SIGKILL` all resolve). Unlike ParseSignal it accepts KILL and rejects every
// non-termination signal, so a typo or an inappropriate signal fails validation
// instead of silently sending the wrong thing.
func ParseKillSignal(name string) (syscall.Signal, error) {
	key := strings.ToUpper(strings.TrimSpace(name))
	key = strings.TrimPrefix(key, signalNamePrefix)
	if sig, ok := killSignalNames[key]; ok {
		return sig, nil
	}
	return 0, fmt.Errorf("kill signal %q: only TERM or KILL are valid", name)
}
