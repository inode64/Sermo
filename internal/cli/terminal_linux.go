//go:build linux

package cli

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	target, err := os.Readlink(filepath.Join("/proc/self/fd", strconv.Itoa(int(f.Fd()))))
	if err != nil {
		return false
	}
	target = filepath.Clean(target)
	return strings.HasPrefix(target, "/dev/pts/") ||
		strings.HasPrefix(target, "/dev/tty") ||
		target == "/dev/console"
}
