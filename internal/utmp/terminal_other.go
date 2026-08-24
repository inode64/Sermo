//go:build !linux

package utmp

import "time"

// TTYPath is unavailable off Linux with utmp.
func TTYPath(string, string) (string, bool) { return "", false }

// TerminalInfo is unavailable off Linux with utmp.
func TerminalInfo(string, string) (Terminal, error) { return Terminal{}, errUnsupported }

// TerminalAccessedAt is unavailable off Linux with /dev/pts.
func TerminalAccessedAt(string, uint64) (time.Time, bool) { return time.Time{}, false }
