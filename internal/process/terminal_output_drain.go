package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"sermo/internal/hostfs"
)

const (
	outputDirectoryMode = 0o700
	outputFileMode      = 0o600
	outputCaptureLimit  = 8 << 20
	outputDrainChunk    = 32 << 10
	outputDrainTimeout  = 30 * time.Second
	outputPollInterval  = 100 * time.Millisecond
)

// ErrTerminalOutputChanged means the observed wait cycle no longer authorizes
// recovery. It is a skipped action, not evidence that a different job is stuck.
var ErrTerminalOutputChanged = errors.New("terminal output wait cycle changed")

// OutputDrain reports only bytes actually preserved, not completion of the job.
type OutputDrain struct {
	ReadBytes int64  `json:"read_bytes"`
	LogOffset int64  `json:"log_offset"`
	Bytes     int64  `json:"bytes"`
	LogFile   string `json:"log_file"`
	Limited   bool   `json:"limited"`
}

type outputKernel struct {
	open       func(int, int) (int, error)
	get        func(int, int, int) (int, error)
	close      func(int) error
	identity   func(int) (Identity, bool)
	index      func(int) (int, error)
	packetMode func(int) (int, error)
	peer       func(int) (int, error)
	stat       func(int) (unix.Stat_t, error)
	flags      func(int) (int, error)
	read       func(int, []byte) (int, error)
	poll       func(int, time.Duration) (bool, error)
}

func newOutputKernel() outputKernel {
	return outputKernel{
		open: unix.PidfdOpen, get: unix.PidfdGetfd, close: unix.Close, identity: OSReader{}.Identity,
		index:      func(fd int) (int, error) { return unix.IoctlGetInt(fd, unix.TIOCGPTN) },
		packetMode: func(fd int) (int, error) { return unix.IoctlGetInt(fd, unix.TIOCGPKT) },
		peer: func(fd int) (int, error) {
			peer, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(fd), unix.TIOCGPTPEER, uintptr(unix.O_RDONLY|unix.O_NOCTTY|unix.O_NONBLOCK|unix.O_CLOEXEC))
			if errno != 0 {
				return 0, fmt.Errorf("open pinned terminal peer: %w", errno)
			}
			return int(peer), nil
		},
		stat: func(fd int) (unix.Stat_t, error) {
			var s unix.Stat_t
			if err := unix.Fstat(fd, &s); err != nil {
				return s, fmt.Errorf("stat pinned terminal: %w", err)
			}
			return s, nil
		},
		flags: func(fd int) (int, error) { return unix.FcntlInt(uintptr(fd), unix.F_GETFL, 0) },
		read:  unix.Read,
		poll: func(fd int, timeout time.Duration) (bool, error) {
			if fd < 0 || fd > math.MaxInt32 {
				return false, errors.New("terminal descriptor out of range")
			}
			fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN | unix.POLLHUP}}
			n, err := unix.Poll(fds, int(timeout.Milliseconds()))
			if err != nil {
				return false, fmt.Errorf("poll pinned terminal: %w", err)
			}
			return n > 0, nil
		},
	}
}

type outputDrainer struct {
	scan    func(context.Context, []Selector) ([]TerminalOutput, error)
	waiting func(context.Context, TerminalOutput) (bool, error)
	kernel  outputKernel
	capture func(string) (*os.File, int64, error)
}

// DrainTerminalOutput borrows the verified PTY through pidfds. It neither
// signals a process nor writes input, and never changes shared file flags.
func DrainTerminalOutput(ctx context.Context, target TerminalOutput, logFile string) (OutputDrain, error) {
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, outputDrainTimeout)
	defer cancel()
	r := newOutputReader()
	d := outputDrainer{scan: r.scan, kernel: newOutputKernel(), capture: openOutputCapture,
		waiting: func(ctx context.Context, want TerminalOutput) (bool, error) {
			if err := ctx.Err(); err != nil {
				return false, fmt.Errorf("revalidate output recovery: %w", err)
			}
			id, ok := r.reader.Identity(want.Owner.PID)
			if !ok {
				return false, nil
			}
			if outputIdentity(id) != want.Owner || !id.ExeOK {
				return false, ErrTerminalOutputChanged
			}
			data, err := r.read(PIDPath(id.PID, outputWaitChannel))
			if err != nil {
				return false, err
			}
			if strings.TrimSpace(string(data)) != "do_wait" {
				return false, nil
			}
			info, err := r.read(PIDPath(id.PID, outputFDInfo) + "/" + strconv.Itoa(want.MasterFD))
			if errors.Is(err, os.ErrNotExist) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			index, ok := outputCounter(info, "tty-index")
			if !ok || index != uint64(want.TTYIndex) {
				return false, ErrTerminalOutputChanged
			}
			reads, err := r.ioCounter(id.PID, "rchar")
			if err != nil {
				return false, err
			}
			return reads == want.OwnerReadChars, nil
		}}
	result, err := d.drain(ctx, target, logFile)
	if errors.Is(err, context.DeadlineExceeded) && parentCtx.Err() == nil {
		result.Limited = true
		return result, nil
	}
	return result, err
}

func (d outputDrainer) drain(ctx context.Context, target TerminalOutput, logFile string) (result OutputDrain, err error) {
	result.LogFile = logFile
	if target.selector == nil || !target.selector.IsStrictPrincipal() {
		return result, ErrTerminalOutputChanged
	}
	current, err := d.scan(ctx, []Selector{*target.selector})
	if err != nil {
		return result, fmt.Errorf("revalidate terminal output: %w", err)
	}
	if !slices.ContainsFunc(current, func(t TerminalOutput) bool { return t.Evidence() == target.Evidence() }) {
		return result, ErrTerminalOutputChanged
	}
	capture, budget, err := d.capture(logFile)
	if err != nil {
		return result, fmt.Errorf("prepare output capture: %w", err)
	}
	result.LogOffset = outputCaptureLimit - budget
	defer func() {
		if syncErr := capture.Sync(); err == nil && syncErr != nil {
			err = fmt.Errorf("sync recovered output: %w", syncErr)
		}
		if closeErr := capture.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close recovered output: %w", closeErr)
		}
	}()
	fd, release, err := d.kernel.pin(ctx, target)
	if err != nil {
		return result, err
	}
	defer release()
	return d.copyOutput(ctx, fd, target, capture, budget, result)
}

func (d outputDrainer) copyOutput(ctx context.Context, fd int, target TerminalOutput, capture *os.File, budget int64, result OutputDrain) (OutputDrain, error) {
	drainCtx, cancel := context.WithTimeout(ctx, outputDrainTimeout)
	defer cancel()
	buffer := make([]byte, outputDrainChunk)
	for result.Bytes < budget {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("recover terminal output: %w", err)
		}
		if drainCtx.Err() != nil {
			result.Limited = true
			return result, nil
		}
		waiting, err := d.waiting(drainCtx, target)
		if err != nil {
			return result, err
		}
		if !waiting {
			return result, nil
		}
		ready, err := d.kernel.poll(fd, outputPollInterval)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return result, fmt.Errorf("wait for terminal output: %w", err)
		}
		if !ready {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("recover terminal output: %w", err)
		}
		if drainCtx.Err() != nil {
			result.Limited = true
			return result, nil
		}
		done, err := d.preserveChunk(fd, buffer[:min(int64(len(buffer)), budget-result.Bytes)], capture, &result)
		if err != nil {
			return result, err
		}
		if done {
			return result, nil
		}
	}
	result.Limited = true
	return result, nil
}

func (k outputKernel) pin(ctx context.Context, target TerminalOutput) (int, func(), error) {
	var fds []int
	release := func() {
		for _, fd := range fds {
			_ = k.close(fd)
		}
	}
	owner, err := k.openIdentity(target.Owner)
	if err != nil {
		return 0, release, err
	}
	fds = append(fds, owner)
	writer, err := k.openIdentity(target.Writer)
	if err != nil {
		release()
		return 0, func() {}, err
	}
	fds = append(fds, writer)
	master, err := k.get(owner, target.MasterFD, 0)
	if err != nil {
		release()
		return 0, func() {}, fmt.Errorf("borrow terminal master: %w", err)
	}
	fds = append(fds, master)
	fail := func(err error) (int, func(), error) { release(); return 0, func() {}, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	index, err := k.index(master)
	if err != nil || index < 0 || uint64(index) != uint64(target.TTYIndex) {
		return fail(ErrTerminalOutputChanged)
	}
	flags, err := k.flags(master)
	if err != nil {
		return fail(err)
	}
	if flags&unix.O_NONBLOCK == 0 {
		return fail(errors.New("terminal master is blocking; shared flags must remain unchanged"))
	}
	if flags&unix.O_ACCMODE == unix.O_WRONLY {
		return fail(errors.New("terminal master does not permit reading"))
	}
	mode, err := k.packetMode(master)
	if err != nil {
		return fail(fmt.Errorf("verify terminal packet mode: %w", err))
	}
	if mode != 0 {
		return fail(errors.New("packet-mode terminal recovery is not supported"))
	}
	peer, err := k.peer(master)
	if err != nil {
		return fail(fmt.Errorf("pin terminal peer: %w", err))
	}
	fds = append(fds, peer)
	output, err := k.get(writer, target.WriterFD, 0)
	if err != nil {
		return fail(fmt.Errorf("pin writer terminal: %w", err))
	}
	fds = append(fds, output)
	a, err := k.stat(peer)
	if err != nil {
		return fail(err)
	}
	b, err := k.stat(output)
	if err != nil {
		return fail(err)
	}
	if a.Mode&unix.S_IFMT != unix.S_IFCHR || b.Mode&unix.S_IFMT != unix.S_IFCHR || a.Dev != b.Dev || a.Ino != b.Ino || a.Rdev != b.Rdev {
		return fail(ErrTerminalOutputChanged)
	}
	for _, want := range []OutputIdentity{target.Owner, target.Writer} {
		if !k.matches(want) {
			return fail(ErrTerminalOutputChanged)
		}
	}
	return master, release, nil
}

func (k outputKernel) matches(want OutputIdentity) bool {
	id, ok := k.identity(want.PID)
	return ok && id.StartTicksOK && id.ExeOK && outputIdentity(id) == want
}

func (k outputKernel) openIdentity(want OutputIdentity) (int, error) {
	if want.PID <= 1 || want.StartTicks == 0 || want.Exe == "" {
		return 0, ErrTerminalOutputChanged
	}
	fd, err := k.open(want.PID, 0)
	if err != nil {
		return 0, fmt.Errorf("pin output process: %w", err)
	}
	if !k.matches(want) {
		_ = k.close(fd)
		return 0, ErrTerminalOutputChanged
	}
	return fd, nil
}

func openOutputCapture(path string) (*os.File, int64, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, 0, errors.New("output capture path must be absolute and clean")
	}
	if err := os.MkdirAll(filepath.Dir(path), outputDirectoryMode); err != nil {
		return nil, 0, fmt.Errorf("create capture directory: %w", err)
	}
	rawUID := os.Geteuid()
	if rawUID < 0 || uint64(rawUID) > math.MaxUint32 {
		return nil, 0, errors.New("daemon user identifier out of range")
	}
	uid := uint32(rawUID)
	parent, err := hostfs.Lstat(filepath.Dir(path))
	if err != nil {
		return nil, 0, fmt.Errorf("inspect capture directory: %w", err)
	}
	owner, ok := parent.Sys().(*syscall.Stat_t)
	if !ok || !parent.IsDir() || parent.Mode().Perm()&0o022 != 0 || owner.Uid != uid {
		return nil, 0, errors.New("output capture directory must be private to the daemon user")
	}
	f, err := hostfs.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK|unix.O_NOCTTY, outputFileMode)
	if err != nil {
		return nil, 0, fmt.Errorf("open output capture: %w", err)
	}
	fail := func(err error) (*os.File, int64, error) { _ = f.Close(); return nil, 0, err }
	info, err := f.Stat()
	if err != nil {
		return fail(err)
	}
	s, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fail(errors.New("capture file metadata unavailable"))
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || s.Nlink != 1 || s.Uid != uid {
		return fail(errors.New("output capture must be a private, singly linked regular file owned by the daemon"))
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return fail(fmt.Errorf("capture already in use: %w", err))
	}
	info, err = f.Stat()
	if err != nil {
		return fail(err)
	}
	budget := int64(outputCaptureLimit) - info.Size()
	if budget <= 0 {
		return fail(errors.New("output capture size limit reached; rotate the file before retrying"))
	}
	if err := unix.Fallocate(int(f.Fd()), unix.FALLOC_FL_KEEP_SIZE, info.Size(), budget); err != nil {
		return fail(fmt.Errorf("reserve recovery log space: %w", err))
	}
	return f, budget, nil
}

func (d outputDrainer) preserveChunk(fd int, buffer []byte, capture *os.File, result *OutputDrain) (bool, error) {
	n, readErr := d.kernel.read(fd, buffer)
	if n > 0 {
		result.ReadBytes += int64(n)
		written, err := capture.Write(buffer[:n])
		result.Bytes += int64(written)
		if err != nil {
			return false, fmt.Errorf("preserve recovered terminal output: %w", err)
		}
		if written != n {
			return false, io.ErrShortWrite
		}
	}
	if errors.Is(readErr, unix.EAGAIN) || errors.Is(readErr, unix.EINTR) {
		return false, nil
	}
	if errors.Is(readErr, unix.EIO) || (n == 0 && readErr == nil) {
		return true, nil
	}
	if readErr != nil {
		return false, fmt.Errorf("read terminal output: %w", readErr)
	}
	return false, nil
}
