package operation

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/process"
)

// ReapTarget selects exactly one previously displayed process, never its
// descendants or the other members of its service's control group.
type ReapTarget struct {
	PID        int
	StartTicks uint64
	Identity   string
}

// ReapProcess uses manual reap policy and the regular locks, guards and audit
// path, narrowed to one process whose displayed identity is still current.
func (e Engine) ReapProcess(ctx context.Context, target ReapTarget) Result {
	return e.run(ctx, plan{action: actionReap, reap: true, reapTarget: &target})
}

// ReapBlockReason explains the same policy refusal enforced by manual reap.
func (e Engine) ReapBlockReason(proc process.Process) string {
	if !e.ReapSelector.Configured() {
		return "requires reap.kill_only_if with exact executable and real user"
	}
	return (process.KillPolicy{ForceKill: true, KillOnlyIf: e.ReapSelector}).BlockReason(proc, e.reapResolver())
}

func (e Engine) discoverReapTargets(target *ReapTarget, initial bool) ([]process.Process, error) {
	strays, err := e.discoverStrays()
	if err != nil || target == nil {
		return strays, err
	}
	if target.PID <= 1 || target.StartTicks == 0 || target.Identity == "" {
		return nil, errors.New("selected reap requires pid, start_ticks and identity")
	}
	for _, proc := range strays {
		if proc.PID != target.PID {
			continue
		}
		if proc.StartTicks != target.StartTicks || process.SignalIdentity(proc) != target.Identity {
			return nil, errors.New("selected process identity changed; refresh and try again")
		}
		return []process.Process{proc}, nil
	}
	if initial {
		return nil, errors.New("selected process is no longer a stray; refresh and try again")
	}
	// Disappearing from this service's inventory does not prove exit: a process
	// may have moved to another cgroup. Never report that as successful cleanup.
	exited, err := e.sessionExited(target.PID, target.StartTicks)
	if err != nil {
		return nil, err
	}
	if !exited {
		return nil, fmt.Errorf("pid %d survived outside the selected stray inventory", target.PID)
	}
	return nil, nil
}
