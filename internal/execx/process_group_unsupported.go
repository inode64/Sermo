//go:build !linux

package execx

import (
	"fmt"
	"os"
	"os/exec"
)

func prepareCommandProcessGroup(_ *exec.Cmd) {}

func cancelCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	if err := cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill command process: %w", err)
	}
	return nil
}
