package process

import (
	"errors"
	"fmt"
	"os"

	"sermo/internal/hostfs"
)

// GenerationExited distinguishes a vanished or recycled PID from an unreadable
// live process. A zombie has exited; only its parent's bookkeeping remains.
func GenerationExited(pid int, ticks uint64) (bool, error) {
	return generationExited(pid, ticks, (OSReader{ReadTTY: true}).Identity, hostfs.ReadFile)
}

func generationExited(pid int, ticks uint64, identity func(int) (Identity, bool), readFile func(string) ([]byte, error)) (bool, error) {
	if pid <= 1 || ticks == 0 {
		return false, errors.New("invalid process generation")
	}
	if id, ok := identity(pid); ok && id.StartTicksOK {
		return id.StartTicks != ticks || id.State == ProcStateZombie, nil
	}
	_, err := readFile(PIDPath(pid, ProcFileStat))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, fmt.Errorf("cannot verify exit of pid %d", pid)
}
