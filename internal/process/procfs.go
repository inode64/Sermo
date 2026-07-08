package process

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	procRoot        = "/proc"
	procFileCmdline = "cmdline"
	procFileExe     = "exe"
	procFileFD      = "fd"
	procFileCWD     = "cwd"
	procFileRoot    = "root"
	procFileStatus  = "status"

	procStatusStatePrefix = "State:"
	procStatusPPIDPrefix  = "PPid:"
	procStatusUIDPrefix   = "Uid:"
	procStatusGIDPrefix   = "Gid:"
	procLineSeparator     = "\n"
	procStatusFirstField  = 0
	numericIDBase         = 10
	numericIDBits         = 32

	procCmdlineSeparator = "\x00"
	procDeletedSuffix    = " (deleted)"
)

func procPIDPath(pid int, name string) string {
	return filepath.Join(procRoot, strconv.Itoa(pid), name)
}

// ProcFileFD is the /proc/<pid>/fd directory name.
const ProcFileFD = procFileFD

// ProcFileCWD is the /proc/<pid>/cwd symlink name.
const ProcFileCWD = procFileCWD

// ProcFileRoot is the /proc/<pid>/root symlink name.
const ProcFileRoot = procFileRoot

// PIDPath returns a path under /proc/<pid>.
func PIDPath(pid int, name string) string {
	return procPIDPath(pid, name)
}

// TrimDeletedSuffix removes the kernel suffix appended to deleted file links.
func TrimDeletedSuffix(path string) string {
	return strings.TrimSuffix(path, procDeletedSuffix)
}

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
type OSReader struct {
	// LookupUserName maps a UID to a display name. Optional: nil uses native
	// os/user lookups.
	LookupUserName func(uint32) string
}

// PIDs lists numeric entries under /proc.
func (OSReader) PIDs() ([]int, error) {
	entries, err := os.ReadDir(procRoot)
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
func (r OSReader) Identity(pid int) (Identity, bool) {
	ppid, uid, gid, state, ok := readStatus(pid)
	if !ok {
		return Identity{}, false
	}
	id := Identity{
		PID:     pid,
		PPID:    ppid,
		UID:     uid,
		GID:     gid,
		User:    r.userName(uid),
		State:   state,
		Cmdline: readCmdline(pid),
	}
	id.Exe, id.ExeOK = readExe(pid)
	return id, true
}

func (r OSReader) userName(uid uint32) string {
	if r.LookupUserName != nil {
		return r.LookupUserName(uid)
	}
	if name, ok := nativeUserName(uid); ok {
		return name
	}
	return ""
}

func readStatus(pid int) (ppid int, uid, gid uint32, state string, ok bool) {
	data, err := os.ReadFile(procPIDPath(pid, procFileStatus))
	if err != nil {
		return 0, 0, 0, "", false
	}
	var gotPPID, gotUID bool
	for _, line := range strings.Split(string(data), procLineSeparator) {
		switch {
		case strings.HasPrefix(line, procStatusStatePrefix):
			if fields := strings.Fields(strings.TrimPrefix(line, procStatusStatePrefix)); len(fields) > procStatusFirstField {
				state = fields[procStatusFirstField] // single char: R, S, Z, ...
			}
		case strings.HasPrefix(line, procStatusPPIDPrefix):
			if v, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, procStatusPPIDPrefix))); err == nil {
				ppid, gotPPID = v, true
			}
		case strings.HasPrefix(line, procStatusUIDPrefix):
			fields := strings.Fields(strings.TrimPrefix(line, procStatusUIDPrefix))
			if len(fields) > procStatusFirstField {
				if v, err := strconv.ParseUint(fields[procStatusFirstField], numericIDBase, numericIDBits); err == nil {
					uid, gotUID = uint32(v), true
				}
			}
		case strings.HasPrefix(line, procStatusGIDPrefix):
			// real GID is the first field, like Uid; best-effort (not required).
			fields := strings.Fields(strings.TrimPrefix(line, procStatusGIDPrefix))
			if len(fields) > procStatusFirstField {
				if v, err := strconv.ParseUint(fields[procStatusFirstField], numericIDBase, numericIDBits); err == nil {
					gid = uint32(v)
				}
			}
		}
	}
	return ppid, uid, gid, state, gotPPID && gotUID
}

// readExe resolves /proc/<pid>/exe. It returns ok=false when the link cannot be
// read or points at a deleted binary, so such a process never matches an exe
// selector.
func readExe(pid int) (string, bool) {
	target, err := os.Readlink(procPIDPath(pid, procFileExe))
	if err != nil {
		return "", false
	}
	if target == "" || strings.HasSuffix(target, procDeletedSuffix) {
		return "", false
	}
	return filepath.Clean(target), true
}

func readCmdline(pid int) []string {
	data, err := os.ReadFile(procPIDPath(pid, procFileCmdline))
	if err != nil || len(data) == 0 {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(data), procCmdlineSeparator), procCmdlineSeparator)
	return parts
}

// OSUserResolver resolves a selector user name (or numeric id) to a real UID
// through the passwd database.
func OSUserResolver(name string) (uint32, bool) {
	if uid, err := strconv.ParseUint(name, numericIDBase, numericIDBits); err == nil {
		return uint32(uid), true
	}
	return nativeUserID(name)
}

// OSGroupResolver resolves a group name (or numeric gid string) to its GID via
// the OS group database — the group analog of OSUserResolver, native Go.
func OSGroupResolver(name string) (uint32, bool) {
	if gid, err := strconv.ParseUint(name, numericIDBase, numericIDBits); err == nil {
		return uint32(gid), true
	}
	return nativeGroupID(name)
}

// canonicalizePath resolves symlinks where possible, falling back to a lexical
// clean for paths that do not exist on this host.
func canonicalizePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return filepath.Clean(path)
}
