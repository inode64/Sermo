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
	"sermo/internal/strutil"
)

const (
	procFileMaps           = "maps"
	procSelfMountNamespace = "/proc/self/" + ProcFileMountNamespace
	libraryMapFields       = 5
	libraryMapPermissions  = 1
	libraryMapDevice       = 3
	libraryMapInode        = 4
	libraryMapLineLimit    = 1024 * 1024
)

// StaleLibrary identifies a mapped shared object whose installed inode differs.
// It is diagnostic evidence, never permission to signal its PID.
type StaleLibrary struct {
	PID        int            `json:"pid"`
	Exe        string         `json:"exe"`
	UID        uint32         `json:"uid"`
	StartTicks uint64         `json:"start_ticks"`
	Path       string         `json:"path"`
	Mapped     ExecutableFile `json:"mapped"`
	Installed  ExecutableFile `json:"installed"`
	Modified   int64          `json:"modified"`
	Changed    int64          `json:"changed"`
	Missing    bool           `json:"missing"`
	// ChrootedProcess marks a finding read through another root than the
	// process's own: it can alert, never drive an action.
	ChrootedProcess bool `json:"chrooted_process,omitempty"`
	RestartEligible bool `json:"restart_eligible"`
}

// LibraryReport retains findings even when part of a process inventory failed.
// Callers must block automatic actions whenever ScanLibraries returns an error.
type LibraryReport struct {
	Libraries    []StaleLibrary `json:"libraries"`
	Processes    int            `json:"processes"`
	Dependencies []string       `json:"dependencies"`
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
	stat     func(string) (libraryFile, error)
}

// ScanLibraries reads executable .so mappings, including dlopen/PAM modules.
// A nil process list requests a host inventory; a non-nil empty list is empty
// service scope. Each mapping is compared with the file the process itself
// would open now, through /proc/PID/root, so a service in its own mount
// namespace (a systemd unit with PrivateTmp or ProtectSystem) is inspected
// too. A chrooted process is compared through the real root of its mount
// namespace: Sermo's own, or that of a scanned peer in the same namespace. With
// neither (a container), it remains unknown.
func ScanLibraries(ctx context.Context, procs []Process) (LibraryReport, error) {
	// Numeric ownership is sufficient here; avoid NSS/network user lookups in
	// a process inventory whose authority is the kernel's real UID.
	numeric := func(id uint32) string { return strconv.FormatUint(uint64(id), numericIDBase) }
	r := libraryReader{reader: OSReader{LookupUserName: numeric, LookupGroupName: numeric}, readFile: hostfs.ReadFile, stat: statLibraryFile}
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
	roots := &libraryRoots{reader: r, procs: procs}
	for _, proc := range procs {
		if err := ctx.Err(); err != nil {
			failures = append(failures, err)
			break
		}
		libraries, err := r.processLibraries(ctx, proc, roots)
		if err != nil {
			failures = append(failures, fmt.Errorf("pid %d: %w", proc.PID, err))
			continue
		}
		out.Processes += libraries.Processes
		out.Libraries = append(out.Libraries, libraries.Libraries...)
		out.Dependencies = append(out.Dependencies, libraries.Dependencies...)
	}
	out.Dependencies = strutil.SortedUnique(out.Dependencies)
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

// libraryRoots finds, for one scan, the prefix that resolves each process's
// mapped paths.
//
// With Sermo's filesystem root the mount namespace may differ, so paths are
// resolved through /proc/PID/root, as the process sees them. A chroot is the
// opposite case: a dropped-privilege worker (dovecot's imap-login) maps libraries
// it loaded before entering an empty directory, so its own root finds none of
// them. They are read through the real root of its mount namespace instead:
// Sermo's when it shares Sermo's namespace, or through a peer that shares it and
// kept the real root, the unit's own daemon under a sandboxed unit.
type libraryRoots struct {
	reader libraryReader
	procs  []Process
	// byNamespace maps a mount namespace to its real root. It costs a stat of
	// every process, so it is built only when a scan meets a chroot.
	byNamespace map[ExecutableFile]string
}

func (s *libraryRoots) of(pid int) (string, bool) {
	root, rootErr := s.reader.stat(PIDPath(pid, ProcFileRoot))
	ours, oursErr := s.reader.stat("/")
	if rootErr != nil || oursErr != nil {
		return "", false
	}
	if root.identity == ours.identity {
		return PIDPath(pid, ProcFileRoot), true
	}
	namespace, err := s.reader.stat(PIDPath(pid, ProcFileMountNamespace))
	if err != nil {
		return "", false
	}
	if s.byNamespace == nil {
		s.index(ours.identity)
	}
	prefix, known := s.byNamespace[namespace.identity]
	return prefix, known
}

func (s *libraryRoots) index(realRoot ExecutableFile) {
	s.byNamespace = map[ExecutableFile]string{}
	if own, err := s.reader.stat(procSelfMountNamespace); err == nil {
		s.byNamespace[own.identity] = ""
	}
	for _, proc := range s.procs {
		root, rootErr := s.reader.stat(PIDPath(proc.PID, ProcFileRoot))
		namespace, namespaceErr := s.reader.stat(PIDPath(proc.PID, ProcFileMountNamespace))
		if rootErr != nil || namespaceErr != nil || root.identity != realRoot {
			continue
		}
		if _, known := s.byNamespace[namespace.identity]; !known {
			s.byNamespace[namespace.identity] = PIDPath(proc.PID, ProcFileRoot)
		}
	}
}

func (r libraryReader) processLibraries(ctx context.Context, proc Process, roots *libraryRoots) (LibraryReport, error) {
	if alive, err := r.verifyProcess(proc); err != nil || !alive {
		return LibraryReport{}, err
	}
	root, visible := roots.of(proc.PID)
	if !visible {
		return LibraryReport{}, errors.New("filesystem root differs or is unreadable")
	}
	data, err := r.readFile(PIDPath(proc.PID, procFileMaps))
	if err != nil {
		return LibraryReport{}, fmt.Errorf("read memory maps: %w", err)
	}
	mappings, err := parseLibraryMaps(string(data))
	if err != nil {
		return LibraryReport{}, err
	}
	chrooted := root != PIDPath(proc.PID, ProcFileRoot)
	report := LibraryReport{Processes: 1}
	for _, mapping := range mappings {
		if err := ctx.Err(); err != nil {
			return LibraryReport{}, fmt.Errorf("read mapped libraries: %w", err)
		}
		finding, stale, err := r.compareLibrary(proc, root, mapping)
		if err != nil {
			return LibraryReport{}, err
		}
		if stale {
			finding.ChrootedProcess = chrooted
			report.Libraries = append(report.Libraries, finding)
		}
		if path := finding.Path; path != "" && path != proc.Exe && path != proc.ExePrev {
			report.Dependencies = append(report.Dependencies, path)
		}
	}
	if alive, err := r.verifyProcess(proc); err != nil || !alive {
		return LibraryReport{}, err
	}
	if after, visible := roots.of(proc.PID); !visible || after != root {
		return LibraryReport{}, errors.New("filesystem root changed during scan")
	}
	// A peer that exited took its root with it: every path read through it would
	// have looked like a missing library.
	if chrooted && root != "" {
		if _, err := r.stat(root); err != nil {
			return LibraryReport{}, errors.New("filesystem root changed during scan")
		}
	}
	return report, nil
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

func (r libraryReader) compareLibrary(proc Process, root string, mapping libraryMapping) (StaleLibrary, bool, error) {
	path := mapping.path
	// The installed file under the root libraryRoots chose for this process.
	installed, err := r.stat(root + path)
	// A real filename may end in " (deleted)". Never strip that suffix when
	// the unmodified path still identifies the kernel's mapped inode.
	if err == nil && installed.identity == mapping.identity {
		return StaleLibrary{Path: path}, false, nil
	}
	if strings.HasSuffix(path, procDeletedSuffix) {
		path = TrimDeletedSuffix(path)
		installed, err = r.stat(root + path)
	}
	missing := errors.Is(err, os.ErrNotExist)
	if err != nil && !missing {
		return StaleLibrary{}, false, fmt.Errorf("inspect library %s: %w", path, err)
	}
	if !missing && !installed.regular {
		return StaleLibrary{}, false, fmt.Errorf("library %s is not a regular file", path)
	}
	if !missing && installed.identity == mapping.identity {
		return StaleLibrary{Path: path}, false, nil
	}
	if path == proc.Exe || path == proc.ExePrev {
		return StaleLibrary{Path: path}, false, nil
	}
	return StaleLibrary{PID: proc.PID, Exe: proc.Exe, UID: proc.UID, StartTicks: proc.StartTicks, Path: path, Mapped: mapping.identity,
		Installed: installed.identity, Modified: installed.modified, Changed: installed.changed, Missing: missing}, true, nil
}
