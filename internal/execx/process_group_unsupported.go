//go:build !linux

package execx

import (
	"fmt"
	"os"
	"os/exec"
)

func prepareCommandProcessGroup(_ *exec.Cmd) {}

// reapProcessGroup is a no-op without process groups: there is no group to
// collect, so a probe's leftovers are left to the platform.
func reapProcessGroup(_ int) {}

func cancelCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill command process: %w", err)
	}
	return nil
}
