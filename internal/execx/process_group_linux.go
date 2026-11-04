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

// reapProcessGroup kills whatever is left of a finished command's process group.
// A probe is an observation, so nothing it leaves running is wanted: InfluxDB's
// `influxd version` reaches for a D-Bus session bus, finds none and executes
// `dbus-launch`, whose session daemon then outlives the probe forever — one
// leaked daemon and one leaked inotify instance per sampling round, until the
// per-user instance limit is exhausted and the host can no longer start a user
// manager. ESRCH means the group is already empty, which is the normal case.
//
// pgid is the pid of the command, which Setpgid made its group leader. The pid
// is already reaped here, so the theoretical race is a wrapped-around pid that
// has become an unrelated group leader in the microseconds since Wait returned.
func reapProcessGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// cancelCommandProcessGroup kills the group the command was started in and the
// command itself. It never asks the kernel for the command's current group: a
// command shares the daemon's session, so it may have moved into the daemon's
// own group, and killing that would kill the daemon. The command is killed
// through its own process handle in case it left its group.
func cancelCommandProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return os.ErrProcessDone
	}
	// Setpgid made the command its group's leader, so its pid is the group id.
	groupErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(groupErr, syscall.ESRCH) {
		groupErr = nil
	}
	processErr := cmd.Process.Kill()
	if errors.Is(processErr, os.ErrProcessDone) {
		processErr = nil
	}
	if groupErr != nil {
		return fmt.Errorf("kill command process group %d: %w", cmd.Process.Pid, groupErr)
	}
	if processErr != nil {
		return fmt.Errorf("kill command process: %w", processErr)
	}
	return nil
}
