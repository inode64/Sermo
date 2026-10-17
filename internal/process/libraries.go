package process

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"sermo/internal/hostfs"
)

const (
	procFileMaps          = "maps"
	procMountNamespace    = "ns/mnt"
	libraryMapFields      = 5
	libraryMapPermissions = 1
	libraryMapDevice      = 3
	libraryMapInode       = 4
	libraryMapLineLimit   = 1024 * 1024
)

// StaleLibrary identifies a mapped shared object whose installed inode differs.
// It is diagnostic evidence, never permission to signal its PID.
type StaleLibrary struct {
	PID             int            `json:"pid"`
	Exe             string         `json:"exe"`
	UID             uint32         `json:"uid"`
	StartTicks      uint64         `json:"start_ticks"`
	Path            string         `json:"path"`
	Mapped          ExecutableFile `json:"mapped"`
	Installed       ExecutableFile `json:"installed"`
	Modified        int64          `json:"modified"`
	Changed         int64          `json:"changed"`
	Missing         bool           `json:"missing"`
	RestartEligible bool           `json:"restart_eligible"`
}

// LibraryReport retains findings even when part of a process inventory failed.
// Callers must block automatic actions whenever ScanLibraries returns an error.
type LibraryReport struct {
	Libraries []StaleLibrary `json:"libraries"`
	Processes int            `json:"processes"`
}

type libraryFile struct {
	identity ExecutableFile
	modified int64
	changed  int64
	regular  bool
}

type libraryReader struct {
	reader   Reader
	readFile func(string) ([]byte, error)
	readlink func(string) (string, error)
	stat     func(string) (libraryFile, error)
}

// ScanLibraries reads executable .so mappings, including dlopen/PAM modules.
// A nil process list requests a host inventory; a non-nil empty list is empty
// service scope. Foreign mount namespaces and chroots remain unknown rather
// than comparing their paths against unrelated host files.
func ScanLibraries(ctx context.Context, procs []Process) (LibraryReport, error) {
	// Numeric ownership is sufficient here; avoid NSS/network user lookups in
	// a process inventory whose authority is the kernel's real UID.
	numeric := func(id uint32) string { return strconv.FormatUint(uint64(id), numericIDBase) }
	r := libraryReader{reader: OSReader{LookupUserName: numeric, LookupGroupName: numeric}, readFile: hostfs.ReadFile, readlink: hostfs.Readlink, stat: statLibraryFile}
	return r.scan(ctx, procs)
}

func statLibraryFile(path string) (libraryFile, error) {
	// O_PATH cannot execute a library or block opening a device/FIFO as data.
	f, err := hostfs.OpenFile(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return libraryFile{}, fmt.Errorf("open library identity: %w", err)
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return libraryFile{}, fmt.Errorf("stat library identity: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return libraryFile{}, errors.New("file identity unavailable")
	}
	return libraryFile{identity: ExecutableFile{Device: st.Dev, Inode: st.Ino}, modified: info.ModTime().UnixNano(), changed: st.Ctim.Nano(), regular: info.Mode().IsRegular()}, nil
}

func (r libraryReader) scan(ctx context.Context, procs []Process) (LibraryReport, error) {
	if err := ctx.Err(); err != nil {
		return LibraryReport{}, fmt.Errorf("scan libraries: %w", err)
	}
	if procs == nil {
		snapshot, err := Snapshot(r.reader)
		if err != nil {
			return LibraryReport{}, fmt.Errorf("library process inventory: %w", err)
		}
		procs = make([]Process, 0, len(snapshot))
		for _, id := range snapshot {
			if id.State != ProcStateZombie && !protectedKernelProcess(id.PID, id.PPID, id.ExeOK, id.Cmdline) {
				procs = append(procs, toProcess(id, RoleChild, SourceChild))
			}
		}
	} else {
		procs = slices.Clone(procs)
	}
	slices.SortFunc(procs, func(a, b Process) int { return a.PID - b.PID })
	var out LibraryReport
	var failures []error
	for _, proc := range procs {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		libraries, err := r.processLibraries(ctx, proc)
		if err != nil {
			failures = append(failures, fmt.Errorf("pid %d: %w", proc.PID, err))
			continue
		}
		out.Processes++
		out.Libraries = append(out.Libraries, libraries...)
	}
	return out, errors.Join(failures...)
}

func (r libraryReader) verifyProcess(proc Process) (bool, error) {
	var id Identity
	var ok bool
	if checked, supportsErrors := r.reader.(IdentityErrorReader); supportsErrors {
		var err error
		id, ok, err = checked.IdentityWithError(proc.PID)
		if err != nil {
			return false, fmt.Errorf("verify mapped process: %w", err)
		}
		if !ok {
			return false, nil
		} // Confirmed exit is normal inventory churn.
	} else {
		id, ok = r.reader.Identity(proc.PID)
	}
	if !ok || !id.StartTicksOK || proc.StartTicks == 0 || id.StartTicks != proc.StartTicks ||
		id.UID != proc.UID || id.Exe != proc.Exe || id.ExePrev != proc.ExePrev || id.Cgroup != proc.Cgroup {
		return false, errors.New("process identity changed or cannot be verified")
	}
	return true, nil
}

func (r libraryReader) sameRoot(pid int) bool {
	ns, err := r.readlink(PIDPath(pid, procMountNamespace))
	self, selfErr := r.readlink(SelfPath(procMountNamespace))
	root, rootErr := r.stat(PIDPath(pid, ProcFileRoot))
	ours, oursErr := r.stat("/")
	return err == nil && selfErr == nil && ns != "" && ns == self && rootErr == nil && oursErr == nil && root.identity == ours.identity
}

func (r libraryReader) processLibraries(ctx context.Context, proc Process) ([]StaleLibrary, error) {
	if alive, err := r.verifyProcess(proc); err != nil || !alive {
		return nil, err
	}
	if !r.sameRoot(proc.PID) {
		return nil, errors.New("filesystem root or mount namespace differs or is unreadable")
	}
	data, err := r.readFile(PIDPath(proc.PID, procFileMaps))
	if err != nil {
		return nil, fmt.Errorf("read memory maps: %w", err)
	}
	mappings, err := parseLibraryMaps(string(data))
	if err != nil {
		return nil, err
	}
	libraries := make([]StaleLibrary, 0, len(mappings))
	for _, mapping := range mappings {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("read mapped libraries: %w", err)
		}
		finding, stale, err := r.compareLibrary(proc, mapping)
		if err != nil {
			return nil, err
		}
		if stale {
			libraries = append(libraries, finding)
		}
	}
	if alive, err := r.verifyProcess(proc); err != nil || !alive {
		return nil, err
	}
	if !r.sameRoot(proc.PID) {
		return nil, errors.New("filesystem root or mount namespace changed during scan")
	}
	return libraries, nil
}

type libraryMapping struct {
	path     string
	identity ExecutableFile
}

func parseLibraryMaps(data string) ([]libraryMapping, error) {
	var mappings []libraryMapping
	seen := make(map[libraryMapping]bool)
	scanner := bufio.NewScanner(strings.NewReader(data))
	scanner.Buffer(nil, libraryMapLineLimit)
	for scanner.Scan() {
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < libraryMapFields {
			return nil, errors.New("malformed memory map")
		}
		if !strings.Contains(fields[libraryMapPermissions], "x") || len(fields) == libraryMapFields {
			continue
		}
		// Preserve spaces in filenames; maps only escapes newlines as \\012.
		path := line
		for range libraryMapFields {
			path = strings.TrimLeft(path, " \t")
			at := strings.IndexAny(path, " \t")
			if at < 0 {
				return nil, errors.New("malformed mapped path")
			}
			path = path[at:]
		}
		path = strings.TrimLeft(path, " \t")
		clean := TrimDeletedSuffix(path)
		name := filepath.Base(clean)
		if !filepath.IsAbs(clean) || strings.HasPrefix(clean, "/memfd:") || !(strings.HasSuffix(name, ".so") || strings.Contains(name, ".so.")) {
			continue
		}
		if filepath.Clean(clean) != clean || strings.Contains(clean, `\012`) {
			return nil, errors.New("ambiguous mapped library path")
		}
		majorText, minorText, hasDevice := strings.Cut(fields[libraryMapDevice], ":")
		if !hasDevice {
			return nil, errors.New("malformed mapped device")
		}
		major, errMajor := strconv.ParseUint(majorText, 16, 32)
		minor, errMinor := strconv.ParseUint(minorText, 16, 32)
		inode, errInode := strconv.ParseUint(fields[libraryMapInode], 10, 64)
		if errMajor != nil || errMinor != nil || errInode != nil || inode == 0 {
			return nil, errors.New("invalid mapped library identity")
		}
		mapping := libraryMapping{path: path, identity: ExecutableFile{Device: unix.Mkdev(uint32(major), uint32(minor)), Inode: inode}}
		if !seen[mapping] {
			mappings = append(mappings, mapping)
			seen[mapping] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("parse memory maps: %w", err)
	}
	return mappings, nil
}

func (r libraryReader) compareLibrary(proc Process, mapping libraryMapping) (StaleLibrary, bool, error) {
	path := mapping.path
	installed, err := r.stat(path)
	// A real filename may end in " (deleted)". Never strip that suffix when
	// the unmodified path still identifies the kernel's mapped inode.
	if err == nil && installed.identity == mapping.identity {
		return StaleLibrary{}, false, nil
	}
	if strings.HasSuffix(path, procDeletedSuffix) {
		path = TrimDeletedSuffix(path)
		installed, err = r.stat(path)
	}
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return StaleLibrary{}, false, fmt.Errorf("inspect library %s: %w", path, err)
	}
	if !missing && !installed.regular {
		return StaleLibrary{}, false, fmt.Errorf("library %s is not a regular file", path)
	}
	if !missing && installed.identity == mapping.identity {
		return StaleLibrary{}, false, nil
	}
	if path == proc.Exe || path == proc.ExePrev {
		return StaleLibrary{}, false, nil
	}
	return StaleLibrary{PID: proc.PID, Exe: proc.Exe, UID: proc.UID, StartTicks: proc.StartTicks, Path: path, Mapped: mapping.identity,
		Installed: installed.identity, Modified: installed.modified, Changed: installed.changed, Missing: missing}, true, nil
}
