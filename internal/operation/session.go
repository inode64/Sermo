package operation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"sermo/internal/process"
)

const sessionExitPollInterval = 100 * time.Millisecond

func (e Engine) closeResidualSession(ctx context.Context, target SessionTarget, boundary SessionBoundary, result *Result) bool {
	const prefix = "close residual SSH session: "
	if boundary.MonitorPID <= 1 || boundary.MonitorStartTicks == 0 || boundary.Exe == "" {
		return failSession(result, prefix, errors.New("incomplete sudo process identity"))
	}
	// Manual reap authorization, narrowed to this terminal's verified sudo
	// monitor and frontend. Workloads, listeners and other sessions stay outside.
	procs := []process.Process{
		{PID: boundary.MonitorPID, StartTicks: boundary.MonitorStartTicks, Exe: boundary.Exe, ExeOK: true, UID: boundary.UID},
		{PID: target.PID, StartTicks: target.StartTicks, Exe: boundary.Exe, ExeOK: true, UID: boundary.UID},
	}
	resolve := e.reapResolver()
	for _, proc := range procs {
		if !e.ReapSelector.Killable(proc, resolve) {
			return failSession(result, prefix, errors.New("requires matching reap.kill_only_if for the residual sudo processes"))
		}
	}
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
		ForceKill: true, KillOnlyIf: e.ReapSelector,
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
