package process

import (
	"slices"
	"strings"

	"sermo/internal/hostfs"
)

// PIDsByComm scans /proc and returns, in ascending order, the PIDs whose kernel
// command name (/proc/<pid>/comm) equals name. It is a native, dependency-free
// replacement for `pidof`/`pgrep` for the narrow "find a daemon by program name"
// case.
//
// It reads /proc/<pid>/comm, which is world-readable, so it finds a process
// owned by another user (e.g. a root daemon) without ptrace privileges — unlike
// the exe-symlink matching used by process selectors. Note the kernel
// truncates comm to 15 characters (TASK_COMM_LEN-1), so name must be the
// (possibly truncated) comm value, not a longer binary path.
func PIDsByComm(name string) ([]int, error) {
	return pidsByComm(OSReader{}.PIDs, readProcessComm, name)
}

func pidsByComm(pids func() ([]int, error), comm func(int) (string, bool), name string) ([]int, error) {
	visible, err := pids()
	if err != nil {
		return nil, err
	}
	matched := make([]int, 0, len(visible))
	for _, pid := range visible {
		value, ok := comm(pid)
		if ok && value == name {
			matched = append(matched, pid)
		}
	}
	slices.Sort(matched)
	return matched, nil
}

func readProcessComm(pid int) (string, bool) {
	data, err := hostfs.ReadFile(PIDPath(pid, procFileComm))
	if err != nil {
		return "", false // process gone or comm unreadable
	}
	return strings.TrimSpace(string(data)), true
}
