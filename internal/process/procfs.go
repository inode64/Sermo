package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	procRoot        = "/proc"
	procSelf        = "self"
	procFileCmdline = "cmdline"
	procFileExe     = "exe"
	procFileFD      = "fd"
	procFileCWD     = "cwd"
	procFileRoot    = "root"
	procFileIO      = "io"
	procFileStat    = "stat"
	procFileStatm   = "statm"
	procFileStatus  = "status"
	procFileTask    = "task"

	procStatusStatePrefix = "State:"
	procStatusPPIDPrefix  = "PPid:"
	procStatusUIDPrefix   = "Uid:"
	procStatusGIDPrefix   = "Gid:"
	procLineSeparator     = "\n"
	procStatusFirstField  = 0
	procStatTTYIndex      = 4 // tty_nr is stat field 7; post-comm fields start at field 3.
	numericIDBase         = 10
	numericIDBits         = 32

	procCmdlineSeparator = "\x00"
	procDeletedSuffix    = " (deleted)"
)

func procPIDPath(pid int, name string) string {
	return filepath.Join(procRoot, strconv.Itoa(pid), name)
}

func procSelfPath(name string) string {
	return filepath.Join(procRoot, procSelf, name)
}

// ProcFileFD is the /proc/<pid>/fd directory name.
const ProcFileFD = procFileFD

// ProcFileCWD is the /proc/<pid>/cwd symlink name.
const ProcFileCWD = procFileCWD

// ProcFileRoot is the /proc/<pid>/root symlink name.
const ProcFileRoot = procFileRoot

// ProcFileIO is the /proc/<pid>/io file name.
const ProcFileIO = procFileIO

// ProcFileStat is the /proc/<pid>/stat file name.
const ProcFileStat = procFileStat

// ProcFileStatm is the /proc/<pid>/statm file name.
const ProcFileStatm = procFileStatm

// ProcFileStatus is the /proc/<pid>/status file name.
const ProcFileStatus = procFileStatus

// ProcFileTask is the /proc/<pid>/task directory name.
const ProcFileTask = procFileTask

// StatFields reads /proc/<pid>/stat and returns the fields after the ')' that
// closes the comm field (so a comm containing spaces or parentheses cannot
// shift them): index 0 is field 3 (state), utime is index 11, stime index 12,
// starttime index 19. Shared by every /proc stat consumer so the comm-splitting
// subtlety lives in one place.
func StatFields(pid int) ([]string, bool) {
	return statFieldsAt(PIDPath(pid, ProcFileStat))
}

// ThreadStatFields is StatFields for one thread: /proc/<pid>/task/<tid>/stat,
// which carries the same field layout as the process file, so the same indices
// apply. A thread that exits between listing the task directory and this read
// simply reports not-ok, the same as a vanished process.
func ThreadStatFields(pid, tid int) ([]string, bool) {
	return statFieldsAt(filepath.Join(PIDPath(pid, ProcFileTask), strconv.Itoa(tid), ProcFileStat))
}

// statFieldsAt is the shared decoder behind StatFields and ThreadStatFields: the
// comm-splitting subtlety must not be reimplemented per caller.
func statFieldsAt(path string) ([]string, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: /proc/<pid>/stat path built from integer PID
	if err != nil {
		return nil, false
	}
	stat := string(data)
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 || closeParen+1 >= len(stat) {
		return nil, false
	}
	return strings.Fields(stat[closeParen+1:]), true
}

// procStatStartTimeIndex is starttime (stat field 22) in the post-comm fields.
const procStatStartTimeIndex = 19

// StartTicks reads a process's starttime (clock ticks since boot, stat field
// 22); the single decoder shared by the lock prober and the metrics reader.
func StartTicks(pid int) (uint64, bool) {
	fields, ok := StatFields(pid)
	if !ok || len(fields) <= procStatStartTimeIndex {
		return 0, false
	}
	ticks, err := strconv.ParseUint(fields[procStatStartTimeIndex], numericIDBase, 64)
	if err != nil {
		return 0, false
	}
	return ticks, true
}

// PIDPath returns a path under /proc/<pid>.
func PIDPath(pid int, name string) string {
	return procPIDPath(pid, name)
}

// SelfPath returns a path under /proc/self.
func SelfPath(name string) string {
	return procSelfPath(name)
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
	// LookupGroupName maps a GID to a display name. Optional: nil uses native
	// os/user lookups.
	LookupGroupName func(uint32) string
	// ReadTTY adds the controlling-terminal device number from /proc/<pid>/stat
	// to Identity. It is disabled for ordinary service discovery because it adds
	// one procfs read per process; terminal-aware checks opt in explicitly.
	ReadTTY bool
}

// PIDs lists numeric entries under /proc.
func (OSReader) PIDs() ([]int, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", procRoot, err)
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
		Group:   r.groupName(gid),
		State:   state,
		Cmdline: readCmdline(pid),
	}
	if r.ReadTTY {
		id.TTY, id.TTYOK = terminalDevice(pid)
		if !id.TTYOK {
			return Identity{}, false
		}
	}
	id.Exe, id.ExeOK, id.ExePrev = readExe(pid)
	return id, true
}

// terminalDevice reads the controlling-terminal device number (tty_nr, stat
// field 7). A zero device is a successfully observed process without a
// controlling terminal; ok=false means the stat record was unavailable or
// malformed.
func terminalDevice(pid int) (device uint64, ok bool) {
	fields, ok := StatFields(pid)
	if !ok || len(fields) <= procStatTTYIndex {
		return 0, false
	}
	value, err := strconv.ParseInt(fields[procStatTTYIndex], numericIDBase, 64)
	if err != nil || value < 0 {
		return 0, false
	}
	return uint64(value), true
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

func (r OSReader) groupName(gid uint32) string {
	if r.LookupGroupName != nil {
		return r.LookupGroupName(gid)
	}
	if name, ok := nativeGroupName(gid); ok {
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
	for line := range strings.SplitSeq(string(data), procLineSeparator) {
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
//
// A deleted binary additionally reports prev = the path it occupied. That is
// diagnostic only — ok stays false, so the safety rule that an unresolvable exe
// matches nothing and is never signalled is unchanged. Callers that act on prev
// must not treat it as an identity.
func readExe(pid int) (exe string, ok bool, prev string) {
	target, err := os.Readlink(procPIDPath(pid, procFileExe))
	if err != nil || target == "" {
		return "", false, ""
	}
	if strings.HasSuffix(target, procDeletedSuffix) {
		return "", false, filepath.Clean(TrimDeletedSuffix(target))
	}
	return filepath.Clean(target), true, ""
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
