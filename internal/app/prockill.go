package app

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"sermo/internal/process"
)

// pidKiller signals one PID through process.Reaper and process.KillSelector,
// with the TERM→KILL escalation the stop policy uses. It is the single kill
// path for host process watches and for the dashboard's per-process kill, so
// the identity re-check before SIGKILL and the kill/kill-failed events cannot
// drift between them.
type pidKiller struct {
	watch    string
	signaler process.Signaler     // nil -> process.OSSignaler{} (real kill(2))
	resolve  process.UserResolver // nil -> process.DefaultUserLookup().ResolveUser
	sleep    func(time.Duration)  // nil -> process.Wait's cancellable timer
	emit     func(Event)
}

// killTarget is one kill request: the sampled process, the selector that
// authorizes it, how to signal it and how to re-verify it during escalation.
// resample must return the PID's current sample when it still belongs to the
// same population the watch tracks; nil forbids escalation.
type killTarget struct {
	info     ProcInfo
	spec     killSpec
	resample func(pid int) (ProcInfo, bool)
	msg      string
}

// killOutcome summarises what a kill did, for callers that answer an operator.
type killOutcome struct {
	signalled bool // the first signal reached the process
	survived  bool // still present after the kill grace
	message   string
}

// pendingKill is a target whose first signal was delivered and whose
// escalation is still owed: the grace wait, the identity re-check and the
// SIGKILL. A watch that signals many findings in one cycle collects them and
// escalates them together, so the grace periods are waited once, not once per
// finding.
type pendingKill struct {
	target  killTarget
	outcome killOutcome
}

// forWatch names the watch whose events a kill records.
func (k pidKiller) forWatch(watch string) pidKiller {
	k.watch = watch
	return k
}

func (k pidKiller) reaper() process.Reaper {
	return process.Reaper{Signaler: k.signaler, ResolveUser: k.resolve, Sleep: k.sleep}
}

func (k pidKiller) emitEvent(e Event) { emitSafe(k.emit, e) }

// kill signals one target, then — with escalate — waits out the grace period,
// re-verifies the PID's identity (start time) and SIGKILLs a survivor, reporting
// one that outlives even that rather than claiming success. It is signal plus
// escalate for a single target; a watch with many targets runs the two apart.
func (k pidKiller) kill(ctx context.Context, t killTarget) killOutcome {
	pending, owed := k.signal(ctx, t)
	if owed {
		k.escalate(ctx, []*pendingKill{&pending})
	}
	return pending.outcome
}

// signal delivers the target's first signal and reports whether an escalation
// is owed: the signal reached the process, escalate was declared, the signal
// was short of SIGKILL and the target can be re-verified.
func (k pidKiller) signal(ctx context.Context, t killTarget) (pendingKill, bool) {
	first := k.reaper().Signal(ctx, []process.Process{t.info.asProcess()}, t.spec.selector, t.spec.signal)
	message, ok := k.emitSignalResult(t.msg, t.spec, t.spec.signal, first)
	pending := pendingKill{target: t, outcome: killOutcome{signalled: ok, message: message}}
	return pending, ok && t.spec.escalate && t.spec.signal != syscall.SIGKILL && t.resample != nil
}

// escalate finishes every pending kill together: one grace wait for all of
// them, then each PID re-verified and SIGKILLed only while it is still the
// same process in the same population, then one kill grace and the survivors
// reported. Each outcome is written back into its pendingKill.
func (k pidKiller) escalate(ctx context.Context, pending []*pendingKill) {
	if len(pending) == 0 {
		return
	}
	// Wait out the grace period (cancellable), then re-verify each PID still
	// belongs to the watch before escalating — over the wait it may have exited
	// and the number been reused by an unrelated process.
	if err := process.Wait(ctx, k.sleep, longestGrace(pending, func(s killSpec) time.Duration { return s.termTimeout })); err != nil {
		return
	}
	var killed []*pendingKill
	for _, p := range pending {
		t := p.target
		current, present := t.resample(t.info.PID)
		if !present || !current.sameProcessAs(t.info) {
			// Either the PID is gone, or the number now belongs to another process
			// that happens to match the watch. Only the start time tells the two
			// apart, and escalating on a namesake would SIGKILL an innocent process.
			continue
		}
		kill := k.reaper().Signal(ctx, []process.Process{current.asProcess()}, t.spec.selector, syscall.SIGKILL)
		message, ok := k.emitSignalResult(t.msg, t.spec, syscall.SIGKILL, kill)
		p.outcome.message = message
		if ok {
			killed = append(killed, p)
		}
	}
	if len(killed) == 0 {
		return
	}
	// After the kill grace, a PID that still matches is unkillable from here (an
	// uninterruptible sleep, or a zombie whose parent has not reaped it) —
	// surface it rather than claim success, mirroring the reaper's final rediscover.
	if err := process.Wait(ctx, k.sleep, longestGrace(killed, func(s killSpec) time.Duration { return s.killTimeout })); err != nil {
		return
	}
	for _, p := range killed {
		t := p.target
		// Same identity caveat as the escalation above: a namesake that took the
		// PID is not our target surviving, and reporting it as one would raise a
		// kill-failed for a process we never signalled.
		if survivor, present := t.resample(t.info.PID); present && survivor.sameProcessAs(t.info) {
			p.outcome.survived = true
			p.outcome.message = fmt.Sprintf("%s: pid %d survived SIGKILL", t.msg, t.info.PID)
			k.emitEvent(Event{Watch: k.watch, Kind: eventKindKillFailed, Message: p.outcome.message})
		}
	}
}

// longestGrace is the wait a batch shares: the longest its targets declare,
// so no target is re-checked before its own grace has elapsed.
func longestGrace(pending []*pendingKill, grace func(killSpec) time.Duration) time.Duration {
	var longest time.Duration
	for _, p := range pending {
		longest = max(longest, grace(p.target.spec))
	}
	return longest
}

// emitSignalResult records one signal round as a kill or kill-failed event and
// reports whether the signal was delivered.
func (k pidKiller) emitSignalResult(msg string, spec killSpec, sig syscall.Signal, result process.ReapResult) (string, bool) {
	if len(result.Signalled) > 0 {
		pid := result.Signalled[0]
		var message string
		if sig == syscall.SIGKILL && spec.signal != syscall.SIGKILL {
			message = fmt.Sprintf("%s: escalated to SIGKILL for pid %d", msg, pid)
		} else {
			message = fmt.Sprintf("%s: sent %s to pid %d", msg, process.SignalName(sig), pid)
		}
		k.emitEvent(Event{Watch: k.watch, Kind: eventKindKill, Message: message})
		return message, true
	}
	var message string
	if len(result.Failed) > 0 {
		failure := result.Failed[0]
		message = fmt.Sprintf("%s: %s pid %d: %v", msg, process.SignalName(sig), failure.PID, failure.Err)
	} else {
		message = msg + ": pid did not match kill selector"
	}
	k.emitEvent(Event{Watch: k.watch, Kind: eventKindKillFailed, Message: message})
	return message, false
}
