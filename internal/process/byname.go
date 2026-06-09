package process

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// PIDsByComm scans /proc and returns, in ascending order, the PIDs whose kernel
// command name (/proc/<pid>/comm) equals name. It is a native, dependency-free
// replacement for `pidof`/`pgrep` for the narrow "find a daemon by program name"
// case.
//
// It reads /proc/<pid>/comm, which is world-readable, so it finds a process
// owned by another user (e.g. a root daemon) without ptrace privileges — unlike
// the exe-symlink matching used by command_match selectors. Note the kernel
// truncates comm to 15 characters (TASK_COMM_LEN-1), so name must be the
// (possibly truncated) comm value, not a longer binary path.
func PIDsByComm(name string) ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue // not a pid directory (e.g. "self", "net")
		}
		data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "comm"))
		if err != nil {
			continue // process gone or comm unreadable
		}
		if strings.TrimSpace(string(data)) == name {
			pids = append(pids, pid)
		}
	}
	sort.Ints(pids)
	return pids, nil
}
