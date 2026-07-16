package locks

import (
	"errors"
	"syscall"

	"sermo/internal/process"
)

// OSProcessProber probes real processes via signal 0 and /proc.
type OSProcessProber struct{}

// Alive reports whether pid names a live process. kill(pid, 0) succeeds for a
// live process we own; EPERM means it is alive but owned by another user.
func (OSProcessProber) Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// StartTicks reads field 22 (starttime) of /proc/<pid>/stat. The comm field
// (field 2) may contain spaces and parentheses, so parsing resumes after the
// final ')'.
func (OSProcessProber) StartTicks(pid int) (uint64, bool) {
	return process.StartTicks(pid)
}
