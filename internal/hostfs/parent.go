package hostfs

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"

	"golang.org/x/sys/unix"
)

// MountID identifies the kernel mount containing an already-open file. Device
// and inode alone cannot distinguish two bind mounts of the same directory.
func MountID(file *os.File) (uint64, error) {
	var info unix.Statx_t
	err := unix.Statx(int(file.Fd()), "", unix.AT_EMPTY_PATH, unix.STATX_MNT_ID, &info)
	runtime.KeepAlive(file)
	if err != nil {
		return 0, fmt.Errorf("stat pinned mount identity: %w", err)
	}
	if info.Mask&unix.STATX_MNT_ID == 0 {
		return 0, errors.New("kernel did not report the pinned mount identity")
	}
	return info.Mnt_id, nil
}

// OpenParentDirectory follows the kernel parent of an already-open directory.
// The caller owns the descriptor and must authorize every directory it reaches;
// no operator-supplied traversal path is accepted by this entry point.
func OpenParentDirectory(directory *os.File) (*os.File, error) {
	fd, err := unix.Openat(int(directory.Fd()), "..", unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	runtime.KeepAlive(directory)
	if err != nil {
		return nil, fmt.Errorf("open pinned directory parent: %w", err)
	}
	return os.NewFile(uintptr(fd), "/proc/self/fd/"+strconv.Itoa(fd)), nil
}
