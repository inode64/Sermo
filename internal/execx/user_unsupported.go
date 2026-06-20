//go:build !linux

package execx

import (
	"fmt"
	"os/exec"
)

func prepareCommandUser(_ *exec.Cmd, userName string) error {
	return fmt.Errorf("execx: command user %q is only supported on linux", userName)
}
