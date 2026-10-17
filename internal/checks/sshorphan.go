package checks

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"sermo/internal/process"
	"sermo/internal/utmp"
)

// recoverOrphanSessions handles split OpenSSH session servers that survive the
// listener. utmp, the terminal ancestry, an exact helper path and real UID, and
// the live configured listener's non-root cgroup must all agree. A process title
// or cgroup alone never authorizes closing a session.
func (s sshSessionOwnership) recoverOrphanSessions(sample *SSHSessionSample, sessions []utmp.Session, terminal func(string) (utmp.Terminal, error), now time.Time) {
	sample.Issues = slices.DeleteFunc(sample.Issues, func(issue SSHSessionIssue) bool {
		for _, session := range sessions {
			if session.Line != issue.Terminal || !issue.Remote {
				continue
			}
			info, err := terminal(session.Line)
			if err != nil || info.Device == 0 {
				return false
			}
			leader, ok := s.orphanSSHLeader(session, info.Device)
			if !ok {
				return false
			}
			sample.SSH = append(sample.SSH, SSHSession{
				PID: leader.PID, StartTicks: leader.StartTicks, Exe: leader.Exe, ExeOK: leader.ExeOK,
				ExePrev: leader.ExePrev, ExeFile: leader.ExeFile, Cgroup: leader.Cgroup, UID: leader.UID,
				User: session.User, Terminal: session.Line, Idle: max(now.Sub(info.AccessedAt), 0),
			})
			return true
		}
		return false
	})
}

func splitSSHExecutable(id process.Identity) bool {
	if !id.ExeOK && (id.ExePrev == "" || id.ExeFile.Inode == 0) {
		return false
	}
	switch cmp.Or(id.Exe, id.ExePrev) {
	case "/usr/lib64/misc/sshd-session", "/usr/lib/misc/sshd-session", "/usr/lib/openssh/sshd-session", "/usr/libexec/openssh/sshd-session", "/usr/libexec/sshd-session", "/usr/lib/sshd-session":
		return true
	}
	return false
}

func (s sshSessionOwnership) orphanSSHLeader(session utmp.Session, tty uint64) (process.Identity, bool) {
	leader, ok := s.snapshot[session.PID]
	if !ok || session.Host == "" || !strings.HasPrefix(session.Line, "pts/") || leader.PPID != 1 ||
		!leader.TTYOK || leader.TTY != 0 || !leader.StartTicksOK || leader.StartTicks == 0 ||
		leader.State == process.ProcStateZombie || !splitSSHExecutable(leader) {
		return process.Identity{}, false
	}
	group := s.cgroup(leader.PID)
	if group == "" || !s.trustedSSHOwner(group, leader.UID) {
		return process.Identity{}, false
	}
	uid, known := s.resolveUser(session.User)
	if !known {
		return process.Identity{}, false
	}
	for _, id := range terminalProcesses(s.snapshot, tty) {
		if id.UID == uid && s.orphanSSHAncestry(id, leader, group) {
			leader.Cgroup = "0::" + group
			return leader, true
		}
	}
	return process.Identity{}, false
}

func (s sshSessionOwnership) trustedSSHOwner(group string, uid uint32) bool {
	for _, id := range s.snapshot {
		if id.UID != uid || id.State == process.ProcStateZombie {
			continue
		}
		for _, filter := range s.filters {
			if filter.Exe == "" || filter.User == "" {
				continue
			}
			matched, err := filter.Match(id, s.resolveUser, nil)
			if err == nil && matched == process.IdentityMatched && s.cgroup(id.PID) == group {
				return true
			}
		}
	}
	return false
}

func (s sshSessionOwnership) orphanSSHAncestry(id, leader process.Identity, group string) bool {
	seen := make(map[int]bool)
	for id.PID > 1 && !seen[id.PID] {
		seen[id.PID] = true
		if !id.StartTicksOK || id.StartTicks < leader.StartTicks || id.State == process.ProcStateZombie || s.cgroup(id.PID) != group {
			return false
		}
		if id.PID == leader.PID {
			return true
		}
		parent, ok := s.snapshot[id.PPID]
		if !ok {
			return false
		}
		id = parent
	}
	return false
}
