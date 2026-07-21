package checks

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

// fileExistsCheck passes when a path exists. It must point at a
// foreign flag/lock file, never Sermo's own runtime locks.
type fileExistsCheck struct {
	base
	path string
}

func (c fileExistsCheck) Run(_ context.Context) Result {
	start := time.Now()
	if _, err := os.Stat(c.path); err != nil {
		if os.IsNotExist(err) {
			return c.result(false, c.path+" does not exist", start)
		}
		return c.result(false, fmt.Sprintf("stat %s: %v", c.path, err), start)
	}
	return c.result(true, c.path+" exists", start)
}

// fileCheck passes when a path exists and is a regular file.
type fileCheck struct {
	base
	path     string
	nonEmpty bool
}

func (c fileCheck) Run(_ context.Context) Result {
	start := time.Now()
	info, err := os.Stat(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return c.result(false, c.path+" does not exist", start)
		}
		return c.result(false, fmt.Sprintf("stat %s: %v", c.path, err), start)
	}
	if !info.Mode().IsRegular() {
		return c.result(false, c.path+" is not a regular file", start)
	}
	if c.nonEmpty && info.Size() == 0 {
		return c.result(false, c.path+" is empty", start)
	}
	res := c.result(true, c.path+" is a regular file", start)
	res.Data = FileResultData(c.path, info)
	return res
}

const (
	// FileModeFormat renders permission bits the way hooks and readings show them.
	FileModeFormat = "%04o"
	// FileOwnerFormat renders a uid:gid pair for hooks and readings.
	FileOwnerFormat = "%d:%d"
	// FileKindDirectory extends the count kinds with the display-only
	// classification for directories.
	FileKindDirectory = "directory"
	// FileKindOther classifies paths that are neither files, directories nor
	// symlinks (sockets, devices, pipes).
	FileKindOther = "other"
)

// FileKind classifies a file mode for display: symlink, file, directory or other.
func FileKind(mode os.FileMode) string {
	switch {
	case mode&os.ModeSymlink != 0:
		return CountKindSymlink
	case mode.IsRegular():
		return CountKindFile
	case mode.IsDir():
		return FileKindDirectory
	default:
		return FileKindOther
	}
}

// FileResultData is the persisted reading data for one inspected path, shared
// by the file check and the live file watch view.
func FileResultData(path string, info os.FileInfo) map[string]any {
	data := map[string]any{
		DataKeyPath:       path,
		DataKeyKind:       FileKind(info.Mode()),
		DataKeySize:       info.Size(),
		DataKeyMode:       fmt.Sprintf(FileModeFormat, info.Mode().Perm()),
		DataKeyModifiedAt: info.ModTime().UTC().Format(time.RFC3339),
	}
	if sys, ok := info.Sys().(*syscall.Stat_t); ok {
		data[CheckKeyOwner] = fmt.Sprintf(FileOwnerFormat, sys.Uid, sys.Gid)
	}
	return data
}
