package operation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sermo/internal/checks"
	"sermo/internal/process"
)

const sessionExitPollInterval = 100 * time.Millisecond

func (e Engine) closeResidualSession(ctx context.Context, target SessionTarget, boundary SessionBoundary, result *Result) bool {
	const prefix = "close residual SSH session: "
	if boundary.MonitorPID <= 1 || boundary.MonitorStartTicks == 0 || boundary.Exe == "" {
		return failSession(result, prefix, errors.New("incomplete sudo process identity"))
	}
	// The verified session evidence is the authority, as for a connected session
	// close: a remote utmp leader reparented to PID 1, the exact sudo executable,
	// the utmp user's real UID and a monitor child in the sshd listener's cgroup.
	// Only this terminal's frontend and monitor are members; the pidfd revalidates
	// each generation, and the verifier the whole boundary, before every signal.
	if !checks.IsSudoExecutable(boundary.Exe) {
		return failSession(result, prefix, fmt.Errorf("residual executable %s is not sudo", boundary.Exe))
	}
	procs := []process.Process{
		{PID: boundary.MonitorPID, StartTicks: boundary.MonitorStartTicks, Exe: boundary.Exe, ExeOK: true, UID: boundary.UID},
		{PID: target.PID, StartTicks: target.StartTicks, Exe: boundary.Exe, ExeOK: true, UID: boundary.UID},
	}
	verified := func(p process.Process) bool { return process.ContainsGeneration(procs, p) }
	reaper := e.Reaper
	reaper.Sleep, reaper.Signaler = e.Sleep, e.SessionSignaler
	var verificationErr error
	reaper.Rediscover = func() []process.Process {
		var remaining []process.Process
		for _, proc := range procs {
			ticks := target.StartTicks
			if proc.PID == boundary.MonitorPID {
				ticks = boundary.MonitorStartTicks
			}
			gone, err := e.sessionExited(proc.PID, ticks)
			if err != nil {
				verificationErr = err
				return nil
			}
			if !gone {
				remaining = append(remaining, proc)
			}
		}
		if len(remaining) == 0 {
			return nil
		}
		current, err := e.SessionVerifier(ctx, target)
		if err != nil {
			verificationErr = err
			return nil
		}
		if current != boundary {
			verificationErr = errors.New("sudo identity changed before escalation")
			return nil
		}
		return remaining
	}
	if err := ctx.Err(); err != nil {
		return failSession(result, prefix, err)
	}
	outcome := reaper.Reap(ctx, procs, process.KillPolicy{
		ForceKill: true, Member: verified,
		TermTimeout: e.KillPolicy.TermTimeout, KillTimeout: e.KillPolicy.KillTimeout,
	})
	if verificationErr != nil {
		return failSession(result, prefix, verificationErr)
	}
	if err := ctx.Err(); err != nil {
		return failSession(result, prefix, err)
	}
	if len(outcome.Failed) > 0 {
		return failSession(result, prefix, outcome.Failed[0].Err)
	}
	if !outcome.OK() {
		return failSession(result, prefix, errors.New("sudo processes survived cleanup"))
	}
	return true
}

func (e Engine) sessionExited(pid int, ticks uint64) (bool, error) {
	exited := e.SessionExited
	if exited == nil {
		exited = process.GenerationExited
	}
	return exited(pid, ticks)
}

func (e Engine) waitSessionExit(ctx context.Context, target SessionTarget, result *Result) bool {
	for {
		gone, err := e.sessionExited(target.PID, target.StartTicks)
		if err != nil {
			return failSession(result, "close SSH session: ", err)
		}
		if gone {
			return true
		}
		if err := process.Wait(ctx, e.Sleep, sessionExitPollInterval); err != nil {
			return failSession(result, "close SSH session: ", fmt.Errorf("pid %d did not exit: %w", target.PID, err))
		}
	}
}
