//go:build !linux

package utmp

import (
	"errors"
	"time"
)

// errUnsupported is returned off Linux, where there is no utmp database.
var errUnsupported = errors.New("utmp is only available on Linux")

// Sessions reports that utmp is unavailable on non-Linux platforms.
func Sessions() ([]Session, error) { return nil, errUnsupported }

// DefaultPaths returns no paths off Linux, where utmp is unavailable.
func DefaultPaths() []string { return nil }

// SessionsFrom reports that utmp is unavailable on non-Linux platforms.
func SessionsFrom([]string) ([]Session, error) { return nil, errUnsupported }

// Terminal is unavailable off Linux with utmp.
type Terminal struct {
	Device     uint64
	AccessedAt time.Time
}

// TTYPath is unavailable off Linux with utmp.
func TTYPath(string, string) (string, bool) { return "", false }

// TerminalInfo is unavailable off Linux with utmp.
func TerminalInfo(string, string) (Terminal, error) { return Terminal{}, errUnsupported }

// TerminalAccessedAt is unavailable off Linux with /dev/pts.
func TerminalAccessedAt(string, uint64) (time.Time, bool) { return time.Time{}, false }
