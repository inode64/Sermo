package checks

import (
	"context"
	"errors"
	"fmt"

	"sermo/internal/process"
)

// TerminalResidualSamplerFunc observes screen servers after their client was removed.
type TerminalResidualSamplerFunc func(context.Context, TerminalSessionConfig) ([]process.Process, error)

// NewTerminalResidualSampler shares the terminal snapshot for monitoring. Manual
// close must use a fresh reader; the signaler pins and revalidates the executable.
func NewTerminalResidualSampler(reader process.Reader, lookup *process.UserLookup) TerminalResidualSamplerFunc {
	reader, lookup = terminalSessionDeps(reader, lookup)
	return func(ctx context.Context, config TerminalSessionConfig) ([]process.Process, error) {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("read residual screen processes: %w", err)
		}
		uid, ok := lookup.ResolveUser(config.User)
		if !ok {
			return nil, fmt.Errorf("resolve screen user %q", config.User)
		}
		snapshot, err := process.Snapshot(reader)
		if err != nil {
			return nil, fmt.Errorf("read residual screen processes: %w", err)
		}
		var procs []process.Process
		for _, id := range snapshot {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("read residual screen processes: %w", err)
			}
			// A screen client and its children are not a session boundary. The
			// daemonized server has no controlling terminal and was adopted by init.
			if config.Multiplexer != TerminalMultiplexerScreen || id.PID <= 1 || id.PPID != 1 ||
				!id.TTYOK || id.TTY != 0 || id.UID != uid || id.State == process.ProcStateZombie ||
				!id.StartTicksOK || id.StartTicks == 0 || id.ExeOK || id.ExePrev != config.Binary || id.ExeFile.Inode == 0 {
				continue
			}
			procs = append(procs, process.Process{
				PID: id.PID, PPID: id.PPID, UID: id.UID, StartTicks: id.StartTicks,
				ExePrev: id.ExePrev, ExeFile: id.ExeFile, Cgroup: id.Cgroup,
			})
		}
		return procs, nil
	}
}

func (c TerminalSessionConfig) residualProcesses(ctx context.Context) ([]process.Process, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Multiplexer != TerminalMultiplexerScreen {
		return nil, errors.New("residual process close requires a screen source")
	}
	sample := c.ResidualProcesses
	if sample == nil {
		sample = NewTerminalResidualSampler(nil, nil)
	}
	return sample(ctx, c)
}

func residualScreenSession(config TerminalSessionConfig, proc process.Process) TerminalSession {
	return TerminalSession{
		Multiplexer: config.Multiplexer, User: config.User,
		Name: fmt.Sprintf("%d.residual", proc.PID), State: TerminalSessionStateResidual,
		Identity: fmt.Sprintf("%s%d:%d:%d:%d:%d", TerminalSessionResidualPrefix,
			proc.PID, proc.StartTicks, proc.UID, proc.ExeFile.Device, proc.ExeFile.Inode),
		PIDs: []int{proc.PID},
	}
}

// VerifyResidualTerminalSession returns freshly observed signal evidence. The
// caller runs through the operation engine; this verifier never sends a signal.
func VerifyResidualTerminalSession(ctx context.Context, config TerminalSessionConfig, want TerminalSession) (process.Process, error) {
	if want.Multiplexer != config.Multiplexer || want.User != config.User || want.Name == "" || want.Identity == "" {
		return process.Process{}, errors.New("invalid residual terminal session target")
	}
	procs, err := config.residualProcesses(ctx)
	if err != nil {
		return process.Process{}, err
	}
	for _, proc := range procs {
		current := residualScreenSession(config, proc)
		if current.Name == want.Name && current.Identity == want.Identity {
			return proc, nil
		}
	}
	return process.Process{}, errors.New("residual screen session changed or disappeared; refresh and try again")
}
