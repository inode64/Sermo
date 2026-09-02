package app

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// SelfStrayHygiene terminates whatever a previous sermod incarnation left behind
// in sermod's own init unit control group.
//
// This is the one place Sermo signals a process it cannot name, and the
// attribution is the strongest available anywhere: the kernel says the process is
// in this unit's control group, and the caller runs it before the daemon has
// spawned anything, so "in my cgroup and not me" can only be a survivor of an
// earlier generation. A unit configured KillMode=process or KillMode=none leaves
// exactly those behind on every restart — the leaked session buses that exhausted
// bk1's inotify instances were 1920 of them.
//
// It is deliberately narrow: only the daemon's own cgroup, only SIGTERM (a
// leftover that ignores it is reported and left alone), one event per process
// signalled, and nothing at all unless the cgroup is a systemd service unit —
// started from a login shell sermod shares its scope with the operator's shell
// and sshd, so SelfUnitCgroupPIDs answers "no" there rather than guessing.
//
// Being a service unit is necessary but not sufficient: the unit must also be
// *sermod's own*. Run inside a unit named for something else — a CI agent's
// service, a container supervisor, a systemd-run wrapper — the processes
// sharing the cgroup belong to that something else, and "in my cgroup and not
// me" attributes its workers to us. That is not hypothetical: the daemon's own
// test suite boots run() inside GitHub's runner service, where this hygiene
// SIGTERMed the runner agent and took the whole machine down with it. A unit
// whose name does not start with the daemon's own is therefore left untouched.
type SelfStrayHygiene struct {
	// ReadFile reads /proc/self/cgroup and the cgroup's process list; nil uses
	// os.ReadFile.
	ReadFile func(string) ([]byte, error)
	// Self is the daemon's own PID; 0 uses os.Getpid().
	Self int
	// Identity names a leftover in its event; nil reads the host /proc. A PID that
	// cannot be read is still signalled — being unidentifiable is what leftovers
	// often are — and reported without a name.
	Identity func(int) (process.Identity, bool)
	// Signaler delivers the signal; nil uses the real kill(2), which refuses PID 1
	// and kernel processes on its own.
	Signaler process.Signaler
	// Emit records one event per signalled process and per delivery failure.
	Emit func(Event)
}

// ReapOwnStraysEnabled reports whether startup hygiene should run for this
// configuration. It is on unless `engine.reap_own_strays` is explicitly false.
func ReapOwnStraysEnabled(cfg *config.Config) bool {
	return config.EngineBoolDefaultTrue(cfg, config.EngineKeyReapOwnStrays)
}

// Run signals every process in the daemon's own control group except the daemon
// itself, returning how many it signalled. It is a no-op when the daemon is not
// running inside a systemd service unit control group.
func (h SelfStrayHygiene) Run() int {
	pids, unit, ok := servicemgr.SelfUnitCgroupPIDs(h.ReadFile)
	if !ok || !strings.HasPrefix(unit, daemonName) {
		return 0
	}
	self := h.Self
	if self == 0 {
		self = os.Getpid()
	}
	signaler := h.Signaler
	if signaler == nil {
		signaler = process.OSSignaler{}
	}
	signalled := 0
	for _, pid := range pids {
		// PID 1 can never be a leftover of ours, and the signaler would refuse it
		// anyway; skipping it here keeps the refusal out of the event feed.
		if pid == self || pid <= 1 {
			continue
		}
		if err := signaler.Signal(pid, syscall.SIGTERM); err != nil {
			emitSafe(h.Emit, Event{
				Service: daemonName,
				Kind:    eventKindKillFailed,
				Action:  eventActionReapOwnStrays,
				Status:  eventStatusFailed,
				Message: fmt.Sprintf("leftover %s in the %s control group: %v", h.describe(pid), unit, err),
			})
			continue
		}
		signalled++
		emitSafe(h.Emit, Event{
			Service: daemonName,
			Kind:    eventKindKill,
			Action:  eventActionReapOwnStrays,
			Status:  eventStatusOK,
			Message: fmt.Sprintf("sent SIGTERM to leftover %s in the %s control group", h.describe(pid), unit),
		})
	}
	return signalled
}

// describe names one leftover as precisely as /proc allows.
func (h SelfStrayHygiene) describe(pid int) string {
	identity := h.Identity
	if identity == nil {
		identity = process.OSReader{}.Identity
	}
	id, ok := identity(pid)
	if !ok {
		return fmt.Sprintf("pid %d", pid)
	}
	if id.ExeOK && id.Exe != "" {
		return fmt.Sprintf("pid %d (%s)", pid, id.Exe)
	}
	if id.ExePrev != "" {
		return fmt.Sprintf("pid %d (%s, replaced on disk)", pid, id.ExePrev)
	}
	return fmt.Sprintf("pid %d", pid)
}
