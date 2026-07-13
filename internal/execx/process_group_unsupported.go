//go:build !linux

package execx

import (
	"os"
	"os/exec"
)

func prepareCommandProcessGroup(_ *exec.Cmd) {}

func cancelCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	return cmd.Process.Kill()
}
