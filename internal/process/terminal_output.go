package process

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"sermo/internal/hostfs"
)

const (
	outputFDInfo      = "fdinfo"
	outputSyscall     = "syscall"
	outputWaitChannel = "wchan"
	outputStack       = "stack"
	outputStdoutFD    = 1
	outputStderrFD    = 2
)

// OutputIdentity binds a terminal endpoint to a live process generation.
type OutputIdentity struct {
	PID        int    `json:"pid"`
	StartTicks uint64 `json:"start_ticks"`
	Exe        string `json:"exe"`
	UID        uint32 `json:"uid"`
	Cgroup     string `json:"cgroup,omitempty"`
}

// TerminalOutput is a verified wait cycle: the sole master holder waits for
// a child whose descendant cannot finish writing to that master's terminal.
// Progress counters participate in the check's unchanged-evidence window.
type TerminalOutput struct {
	Owner            OutputIdentity `json:"owner"`
	Writer           OutputIdentity `json:"writer"`
	MasterFD         int            `json:"master_fd"`
	WriterFD         int            `json:"writer_fd"`
	TTYIndex         uint32         `json:"tty_index"`
	OwnerReadChars   uint64         `json:"owner_read_chars"`
	WriterWriteChars uint64         `json:"writer_write_chars"`
	selector         *Selector
}

// Evidence excludes the in-memory authority when comparing observations.
// Deserializing dashboard data cannot recreate permission to drain a terminal.
func (t TerminalOutput) Evidence() TerminalOutput { t.selector = nil; return t }

type outputMaster struct {
	pid, fd int
	index   uint32
}

type outputReader struct {
	reader  Reader
	read    func(string) ([]byte, error)
	dirs    func(string) ([]os.DirEntry, error)
	link    func(string) (string, error)
	stat    func(string) (os.FileInfo, error)
	resolve UserResolver
}

func newOutputReader() outputReader {
	return outputReader{reader: OSReader{}, read: hostfs.ReadFile, dirs: hostfs.ReadDir,
		link: hostfs.Readlink, stat: hostfs.StatTarget, resolve: DefaultUserLookup().ResolveUser}
}

// ScanTerminalOutput observes only; it never reads bytes from a terminal.
func ScanTerminalOutput(ctx context.Context, selectors []Selector) ([]TerminalOutput, error) {
	return newOutputReader().scan(ctx, selectors)
}

func (r outputReader) scan(ctx context.Context, selectors []Selector) ([]TerminalOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("observe terminal output: %w", err)
	}
	if len(selectors) == 0 {
		return nil, errors.New("terminal output requires strict process selectors")
	}
	for _, s := range selectors {
		if !s.IsStrictPrincipal() {
			return nil, errors.New("terminal output requires exact executable and user; delegated selectors are not supported")
		}
	}
	snapshot, err := Snapshot(r.reader)
	if err != nil {
		return nil, fmt.Errorf("observe terminal processes: %w", err)
	}
	idx := buildSnapshotIndex(snapshot)
	d := Discoverer{Reader: r.reader, ResolveUser: r.resolve}
	matches := func(pid int) bool { id, ok := snapshot[pid]; return ok && d.matchesAny(selectors, id, r.resolve) }
	var roots []int
	for _, pid := range idx.sorted {
		if pid > 1 && matches(pid) && !hasAncestor(idx, pid, matches) {
			candidate, err := r.potentialOutputCycle(ctx, idx, pid)
			if err != nil {
				return nil, err
			}
			if candidate {
				roots = append(roots, pid)
			}
		}
	}
	if len(roots) == 0 {
		return nil, nil
	}
	masters, err := r.masters(ctx, idx.sorted)
	if err != nil {
		return nil, err
	}
	var out []TerminalOutput
	for _, pid := range roots {
		var authority *Selector
		for i := range selectors {
			if d.matches(&selectors[i], snapshot[pid], r.resolve) {
				authority = &selectors[i]
				break
			}
		}
		found, err := r.ownerOutput(ctx, idx, snapshot[pid], masters, authority)
		if err != nil {
			return nil, fmt.Errorf("inspect terminal output owner %d: %w", pid, err)
		}
		out = append(out, found...)
	}
	return out, nil
}

// Ordinary daemons and progressing jobs never need the host-wide FD scan.
func (r outputReader) potentialOutputCycle(ctx context.Context, idx *snapshotIndex, pid int) (bool, error) {
	child, waiting, err := r.waitedChild(pid, idx.children[pid])
	if err != nil || !waiting {
		return false, err
	}
	for _, writer := range append([]int{child}, descendants(idx.children, []int{child})...) {
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("observe output writers: %w", err)
		}
		_, blocked, err := r.blockedWriter(writer)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return false, err
		}
		if blocked {
			return true, nil
		}
	}
	return false, nil
}

func (r outputReader) masters(ctx context.Context, pids []int) ([]outputMaster, error) {
	var out []outputMaster
	for _, pid := range pids {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("observe terminal descriptors: %w", err)
		}
		entries, err := r.dirs(PIDPath(pid, ProcFileFD))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("list terminal descriptors for pid %d: %w", pid, err)
		}
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("observe terminal descriptors: %w", err)
			}
			fd, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			master, present, err := r.master(pid, fd)
			if err != nil {
				return nil, err
			}
			if present {
				out = append(out, master)
			}
		}
	}
	return out, nil
}

func (r outputReader) master(pid, fd int) (outputMaster, bool, error) {
	name := strconv.Itoa(fd)
	path, err := r.link(PIDPath(pid, ProcFileFD) + "/" + name)
	if errors.Is(err, fs.ErrNotExist) {
		return outputMaster{}, false, nil
	}
	if err != nil {
		return outputMaster{}, false, fmt.Errorf("read terminal descriptor: %w", err)
	}
	if path != "/dev/ptmx" && path != "/dev/pts/ptmx" {
		return outputMaster{}, false, nil
	}
	data, err := r.read(PIDPath(pid, outputFDInfo) + "/" + name)
	if err != nil {
		return outputMaster{}, false, fmt.Errorf("read terminal master identity: %w", err)
	}
	value, ok := outputCounter(data, "tty-index")
	if !ok || value > uint64(^uint32(0)) {
		return outputMaster{}, false, errors.New("terminal master index unavailable")
	}
	return outputMaster{pid: pid, fd: fd, index: uint32(value)}, true, nil
}

func (r outputReader) ownerOutput(ctx context.Context, idx *snapshotIndex, owner Identity, masters []outputMaster, authority *Selector) ([]TerminalOutput, error) {
	waitFor, waiting, err := r.waitedChild(owner.PID, idx.children[owner.PID])
	if err != nil || !waiting {
		return nil, err
	}
	var out []TerminalOutput
	for _, master := range masters {
		if master.pid != owner.PID || !uniqueOutputMaster(masters, master) {
			continue
		}
		found, err := r.masterOutput(ctx, idx, owner, waitFor, master, authority)
		if err != nil {
			return nil, err
		}
		out = append(out, found...)
	}
	return out, nil
}

func (r outputReader) masterOutput(ctx context.Context, idx *snapshotIndex, owner Identity, waitFor int, master outputMaster, authority *Selector) ([]TerminalOutput, error) {
	peer, err := r.stat(filepath.Join(PIDPath(owner.PID, ProcFileRoot), "dev", "pts", strconv.FormatUint(uint64(master.index), 10)))
	if err != nil {
		return nil, fmt.Errorf("inspect terminal peer: %w", err)
	}
	for _, pid := range append([]int{waitFor}, descendants(idx.children, []int{waitFor})...) {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("inspect output writer: %w", err)
		}
		writer := idx.byPID[pid]
		fd, blocked, err := r.blockedWriter(pid)
		if err != nil {
			return nil, err
		}
		if !blocked {
			continue
		}
		info, err := r.stat(PIDPath(pid, ProcFileFD) + "/" + strconv.Itoa(fd))
		if err != nil {
			return nil, fmt.Errorf("inspect writer terminal: %w", err)
		}
		if !sameOutputDevice(peer, info) {
			continue
		}
		reads, err := r.ioCounter(owner.PID, "rchar")
		if err != nil {
			return nil, err
		}
		writes, err := r.ioCounter(pid, "wchar")
		if err != nil {
			return nil, err
		}
		if !owner.ExeOK || !writer.ExeOK || !owner.StartTicksOK || !writer.StartTicksOK {
			return nil, errors.New("terminal process identity unavailable")
		}
		return []TerminalOutput{{Owner: outputIdentity(owner), Writer: outputIdentity(writer), MasterFD: master.fd, WriterFD: fd, TTYIndex: master.index, OwnerReadChars: reads, WriterWriteChars: writes, selector: authority}}, nil
	}
	return nil, nil
}

func (r outputReader) waitedChild(pid int, children []int) (int, bool, error) {
	threads, err := r.dirs(PIDPath(pid, ProcFileTask))
	if err != nil {
		return 0, false, err
	}
	if len(threads) != 1 {
		return 0, false, nil
	}
	wchan, err := r.read(PIDPath(pid, outputWaitChannel))
	if err != nil {
		return 0, false, err
	}
	if strings.TrimSpace(string(wchan)) != "do_wait" {
		return 0, false, nil
	}
	fields, err := r.syscall(pid)
	if err != nil {
		return 0, false, err
	}
	if len(fields) < 2 || fields[0] != uint64(unix.SYS_WAIT4) {
		return 0, false, errors.New("unsupported child-wait evidence")
	}
	if fields[1] == ^uint64(0) && len(children) == 1 {
		return children[0], true, nil
	}
	for _, current := range children {
		if current > 0 && uint64(current) == fields[1] {
			return current, true, nil
		}
	}
	return 0, false, nil
}

func (r outputReader) blockedWriter(pid int) (int, bool, error) {
	fields, err := r.syscall(pid)
	if err != nil {
		return 0, false, err
	}
	if len(fields) < 2 || (fields[0] != uint64(unix.SYS_WRITE) && fields[0] != uint64(unix.SYS_WRITEV)) || (fields[1] != 1 && fields[1] != 2) {
		return 0, false, nil
	}
	stack, err := r.read(PIDPath(pid, outputStack))
	if err != nil {
		return 0, false, err
	}
	fd := outputStdoutFD
	if fields[1] == outputStderrFD {
		fd = outputStderrFD
	}
	return fd, strings.Contains(string(stack), "n_tty_write+"), nil
}

func (r outputReader) syscall(pid int) ([]uint64, error) {
	data, err := r.read(PIDPath(pid, outputSyscall))
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return nil, errors.New("empty syscall evidence")
	}
	if fields[0] == "running" || fields[0] == "-1" {
		return nil, nil
	}
	var out []uint64
	for _, field := range fields {
		value, err := strconv.ParseUint(field, 0, 64)
		if err != nil {
			return nil, fmt.Errorf("parse syscall evidence: %w", err)
		}
		out = append(out, value)
	}
	return out, nil
}

func (r outputReader) ioCounter(pid int, key string) (uint64, error) {
	data, err := r.read(PIDPath(pid, ProcFileIO))
	if err != nil {
		return 0, err
	}
	if value, ok := outputCounter(data, key); ok {
		return value, nil
	}
	return 0, errors.New("process I/O counter unavailable")
}

func outputCounter(data []byte, key string) (uint64, bool) {
	for line := range strings.SplitSeq(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == key+":" {
			value, err := strconv.ParseUint(fields[1], 10, 64)
			return value, err == nil
		}
	}
	return 0, false
}

func uniqueOutputMaster(masters []outputMaster, want outputMaster) bool {
	for _, m := range masters {
		if m.index == want.index && (m.pid != want.pid || m.fd != want.fd) {
			return false
		}
	}
	return true
}

func outputIdentity(id Identity) OutputIdentity {
	return OutputIdentity{PID: id.PID, StartTicks: id.StartTicks, Exe: id.Exe, UID: id.UID, Cgroup: id.Cgroup}
}

func sameOutputDevice(a, b os.FileInfo) bool {
	x, ok := a.Sys().(*syscall.Stat_t)
	y, yes := b.Sys().(*syscall.Stat_t)
	return ok && yes && a.Mode()&os.ModeCharDevice != 0 && b.Mode()&os.ModeCharDevice != 0 && x.Dev == y.Dev && x.Ino == y.Ino && x.Rdev == y.Rdev
}
