//go:build linux

package utmp

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Terminal is the kernel identity and last input time of a login terminal.
type Terminal struct {
	Device     uint64
	AccessedAt time.Time
}

// TTYPath resolves a utmp terminal line below devRoot without permitting an
// absolute path or traversal outside that root.
func TTYPath(devRoot, line string) (string, bool) {
	if strings.ContainsRune(line, 0) || filepath.IsAbs(line) {
		return "", false
	}
	clean := filepath.Clean(line)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	root, err := filepath.Abs(devRoot)
	if err != nil {
		return "", false
	}
	path := filepath.Join(root, clean)
	if path != root && strings.HasPrefix(path, root+string(os.PathSeparator)) {
		return path, true
	}
	return "", false
}

// TerminalInfo reads the controlling-terminal device and access time for a
// validated utmp line. Linux updates a terminal's atime when input is read,
// which is the same conventional idle signal exposed by tools such as w.
func TerminalInfo(devRoot, line string) (Terminal, error) {
	path, ok := TTYPath(devRoot, line)
	if !ok {
		return Terminal{}, fmt.Errorf("unsafe terminal line %q", line)
	}
	info, err := os.Stat(path)
	if err != nil {
		return Terminal{}, fmt.Errorf("stat terminal %s: %w", path, err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Terminal{}, fmt.Errorf("stat terminal %s: missing Linux stat data", path)
	}
	return Terminal{
		Device:     stat.Rdev,
		AccessedAt: time.Unix(stat.Atim.Sec, stat.Atim.Nsec),
	}, nil
}

// TerminalAccessedAt resolves a terminal device number to its last-input time.
// Multiplexer sessions expose the device but not necessarily its pts path, so
// this bounded /dev/pts lookup keeps idle attribution native and exact.
func TerminalAccessedAt(devRoot string, device uint64) (time.Time, bool) {
	entries, err := os.ReadDir(filepath.Join(devRoot, "pts"))
	if err != nil {
		return time.Time{}, false
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Name() == "ptmx" {
			continue
		}
		terminal, err := TerminalInfo(devRoot, filepath.Join("pts", entry.Name()))
		if err == nil && terminal.Device == device {
			return terminal.AccessedAt, true
		}
	}
	return time.Time{}, false
}
