package checks

import (
	"fmt"

	"sermo/internal/process"
	"sermo/internal/servicemgr"
	"sermo/internal/utmp"
)

// sshSessionOwnership shares the process snapshot and cgroup evidence used to
// verify sudo residuals and split OpenSSH session servers. A cgroup or utmp
// record alone never authorizes a close.
type sshSessionOwnership struct {
	snapshot    map[int]process.Identity
	filters     []process.IdentityFilter
	resolveUser process.UserResolver
	readFile    func(string) ([]byte, error)
	cgroups     map[int]string
}

func sessionSudo(id process.Identity, uid uint32) bool {
	return id.PID > 1 && id.UID == uid && id.ExeOK &&
		(id.Exe == "/usr/bin/sudo" || id.Exe == "/bin/sudo") &&
		id.StartTicksOK && id.StartTicks > 0 && id.State != process.ProcStateZombie
}

type sshSudoProcesses struct {
	Frontend process.Identity
	Monitor  process.Identity
}

func (s sshSessionOwnership) target(session utmp.Session, processes []process.Identity) (sshSudoProcesses, bool) {
	leader, ok := s.snapshot[session.PID]
	if !ok || session.Host == "" || leader.PPID != 1 || !leader.TTYOK || leader.TTY != 0 {
		return sshSudoProcesses{}, false
	}
	uid, known := s.resolveUser(session.User)
	if !known || !sessionSudo(leader, uid) {
		return sshSudoProcesses{}, false
	}
	for _, monitor := range processes {
		if monitor.PPID != leader.PID || !sessionSudo(monitor, uid) || monitor.Exe != leader.Exe || monitor.StartTicks < leader.StartTicks {
			continue
		}
		group := s.cgroup(leader.PID)
		if group != "" && group == s.cgroup(monitor.PID) && s.trustedSSHGroup(group) {
			return sshSudoProcesses{Frontend: leader, Monitor: monitor}, true
		}
	}
	return sshSudoProcesses{}, false
}

func (s sshSessionOwnership) trustedSSHGroup(group string) bool {
	for _, id := range s.snapshot {
		for _, filter := range s.filters {
			matched, err := filter.Match(id, s.resolveUser, nil)
			if err == nil && matched == process.IdentityMatched && s.cgroup(id.PID) == group {
				return true
			}
		}
	}
	return false
}

func (s sshSessionOwnership) cgroup(pid int) string {
	if group, cached := s.cgroups[pid]; cached {
		return group
	}
	data, err := s.readFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	group := ""
	if err == nil {
		if path, ok := servicemgr.UnifiedCgroupPath(string(data)); ok && path != "/" {
			group = path
		}
	}
	s.cgroups[pid] = group
	return group
}
