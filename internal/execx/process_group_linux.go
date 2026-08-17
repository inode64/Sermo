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
// has become an unrelated group leader in the microseconds since Wait returned;
// cancelCommandProcessGroup carries the same exposure.
func reapProcessGroup(pgid int) {
	if pgid <= 0 {
		return
	}
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
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
