package process

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
)

// Reader reads process identities from a /proc-like source. It is an interface
// so discovery can be tested without real processes.
type Reader interface {
	// PIDs lists the currently visible process IDs.
	PIDs() ([]int, error)
	// Identity reads one process. ok is false if the process has vanished.
	Identity(pid int) (Identity, bool)
}

// UserResolver maps a selector's user name (or numeric id) to a real UID.
type UserResolver func(name string) (uint32, bool)

// OSReader reads the host /proc filesystem.
type OSReader struct{}

// PIDs lists numeric entries under /proc.
func (OSReader) PIDs() ([]int, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	pids := make([]int, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if pid, err := strconv.Atoi(e.Name()); err == nil {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

// Identity reads PPID, real UID, resolved exe and cmdline for a process.
func (OSReader) Identity(pid int) (Identity, bool) {
	ppid, uid, state, ok := readStatus(pid)
	if !ok {
		return Identity{}, false
	}
	id := Identity{
		PID:     pid,
		PPID:    ppid,
		UID:     uid,
		User:    lookupUser(uid),
		State:   state,
		Cmdline: readCmdline(pid),
	}
	id.Exe, id.ExeOK = readExe(pid)
	return id, true
}

func readStatus(pid int) (ppid int, uid uint32, state string, ok bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0, 0, "", false
	}
	var gotPPID, gotUID bool
	for _, line := range strings.Split(string(data), "\n") {
		switch {
		case strings.HasPrefix(line, "State:"):
			if fields := strings.Fields(strings.TrimPrefix(line, "State:")); len(fields) > 0 {
				state = fields[0] // single char: R, S, Z, ...
			}
		case strings.HasPrefix(line, "PPid:"):
			if v, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:"))); err == nil {
				ppid, gotPPID = v, true
			}
		case strings.HasPrefix(line, "Uid:"):
			fields := strings.Fields(strings.TrimPrefix(line, "Uid:"))
			if len(fields) > 0 {
				if v, err := strconv.ParseUint(fields[0], 10, 32); err == nil {
					uid, gotUID = uint32(v), true
				}
			}
		}
	}
	return ppid, uid, state, gotPPID && gotUID
}

// readExe resolves /proc/<pid>/exe. It returns ok=false when the link cannot be
// read or points at a deleted binary, so such a process never matches an exe
// selector (section 21 fail-safe).
func readExe(pid int) (string, bool) {
	target, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/exe")
	if err != nil {
		return "", false
	}
	if target == "" || strings.HasSuffix(target, " (deleted)") {
		return "", false
	}
	return filepath.Clean(target), true
}

func readCmdline(pid int) []string {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil || len(data) == 0 {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	return parts
}

func lookupUser(uid uint32) string {
	if u, err := user.LookupId(strconv.FormatUint(uint64(uid), 10)); err == nil {
		return u.Username
	}
	return ""
}

// OSUserResolver resolves a selector user name (or numeric id) to a real UID
// through the passwd database.
func OSUserResolver(name string) (uint32, bool) {
	if uid, err := strconv.ParseUint(name, 10, 32); err == nil {
		return uint32(uid), true
	}
	u, err := user.Lookup(name)
	if err != nil {
		return 0, false
	}
	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, false
	}
	return uint32(uid), true
}

// canonicalizePath resolves symlinks where possible, falling back to a lexical
// clean for paths that do not exist on this host.
func canonicalizePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
