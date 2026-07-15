//go:build linux

package execx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func prepareCommandProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

func cancelCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		if err := cmd.Process.Kill(); err != nil {
			return fmt.Errorf("kill command process after process-group lookup: %w", err)
		}
		return nil
	}
	if err := syscall.Kill(-pgid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return fmt.Errorf("kill command process group %d: %w", pgid, err)
	}
	return nil
}
