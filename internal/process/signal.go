package process

import (
	"fmt"
	"sort"
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
func (r Reaper) Reap(residuals []Process, policy KillPolicy) ReapResult {
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
	sleep(policy.TermTimeout)
	residuals = r.Rediscover()
	if len(residuals) == 0 {
		return ReapResult{Signalled: sortedInts(signalled)}
	}

	round(residuals, syscall.SIGKILL)
	sleep(policy.KillTimeout)
	residuals = r.Rediscover()

	return ReapResult{Remaining: residuals, Signalled: sortedInts(signalled)}
}

func sortedInts(set map[int]bool) []int {
	out := make([]int, 0, len(set))
	for pid := range set {
		out = append(out, pid)
	}
	sort.Ints(out)
	return out
}
