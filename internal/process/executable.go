package process

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"sermo/internal/hostfs"
)

const executableBits = 0o111

// openDeletedExecutable follows procfs's magic link, never the old on-disk
// pathname. fstat distinguishes a genuinely unlinked file from a filename that
// happens to end in " (deleted)". The open file stays pinned through signaling.
func openDeletedExecutable(pid int, previous string) (*os.File, ExecutableFile, error) {
	reader := executableReader{open: hostfs.Open, readlink: hostfs.Readlink}
	return reader.openDeleted(pid, previous)
}

type executableReader struct {
	open     func(string) (*os.File, error)
	readlink func(string) (string, error)
}

func (r executableReader) openDeleted(pid int, previous string) (*os.File, ExecutableFile, error) {
	if !filepath.IsAbs(previous) || filepath.Clean(previous) != previous {
		return nil, ExecutableFile{}, fmt.Errorf("invalid previous executable path for pid %d", pid)
	}
	namespace, nsErr := r.readlink(PIDPath(pid, "ns/mnt"))
	self, selfErr := r.readlink(SelfPath("ns/mnt"))
	if nsErr != nil || selfErr != nil || namespace == "" || namespace != self {
		return nil, ExecutableFile{}, fmt.Errorf("cannot verify executable mount namespace for pid %d", pid)
	}
	path := PIDPath(pid, procFileExe)
	f, err := r.open(path)
	if err != nil {
		return nil, ExecutableFile{}, fmt.Errorf("open deleted executable for pid %d: %w", pid, err)
	}
	info, statErr := f.Stat()
	link, linkErr := r.readlink(path)
	if statErr == nil && linkErr == nil && link == previous+procDeletedSuffix && info.Mode().IsRegular() && info.Mode().Perm()&executableBits != 0 {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink == 0 && stat.Ino != 0 {
			return f, ExecutableFile{Device: stat.Dev, Inode: stat.Ino}, nil
		}
	}
	_ = f.Close()
	return nil, ExecutableFile{}, fmt.Errorf("cannot verify unlinked executable for pid %d", pid)
}

func deletedExecutableIdentity(pid int, previous string) ExecutableFile {
	f, identity, err := openDeletedExecutable(pid, previous)
	if err != nil {
		return ExecutableFile{}
	}
	_ = f.Close()
	return identity
}

func pinDeletedExecutable(target Process) (func(), error) {
	f, identity, err := openDeletedExecutable(target.PID, target.ExePrev)
	if err != nil {
		return nil, err
	}
	if identity != target.ExeFile {
		_ = f.Close()
		return nil, fmt.Errorf("executable file changed for pid %d", target.PID)
	}
	return func() { _ = f.Close() }, nil
}
