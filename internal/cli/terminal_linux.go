//go:build linux

package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"

	"sermo/internal/process"
)

const (
	consoleDevicePath = "/dev/console"
	devPtsPrefix      = "/dev/pts/"
	devTTYPrefix      = "/dev/tty"
)

func stdinIsTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	target, err := os.Readlink(filepath.Join(process.SelfPath(process.ProcFileFD), strconv.Itoa(int(f.Fd()))))
	if err != nil {
		return false
	}
	target = filepath.Clean(target)
	return strings.HasPrefix(target, devPtsPrefix) ||
		strings.HasPrefix(target, devTTYPrefix) ||
		target == consoleDevicePath
}

// disableEcho turns off terminal echo so a typed password is not shown, and
// returns the function that restores the previous terminal state. Echo is a
// terminal setting, not a property of the process: leaving it off would follow
// the operator into their next shell command.
func disableEcho(r io.Reader) (func(), error) {
	f, ok := r.(*os.File)
	if !ok {
		return nil, errors.New("standard input is not a terminal")
	}
	fd := int(f.Fd())
	previous, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, fmt.Errorf("read terminal state: %w", err)
	}
	quiet := *previous
	quiet.Lflag &^= unix.ECHO
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &quiet); err != nil {
		return nil, fmt.Errorf("disable terminal echo: %w", err)
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, previous) }, nil
}
