package locks

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
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
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, false
	}
	stat := string(data)
	closeParen := strings.LastIndex(stat, ")")
	if closeParen < 0 || closeParen+1 >= len(stat) {
		return 0, false
	}
	// After ')', fields begin at field 3 (state); starttime (field 22) is the
	// 20th of these (index 19).
	fields := strings.Fields(stat[closeParen+1:])
	const startTimeIndex = 19
	if len(fields) <= startTimeIndex {
		return 0, false
	}
	ticks, err := strconv.ParseUint(fields[startTimeIndex], 10, 64)
	if err != nil {
		return 0, false
	}
	return ticks, true
}
