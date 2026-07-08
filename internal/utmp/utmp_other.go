//go:build !linux

package utmp

import "errors"

// errUnsupported is returned off Linux, where there is no utmp database.
var errUnsupported = errors.New("utmp is only available on Linux")

// Sessions reports that utmp is unavailable on non-Linux platforms.
func Sessions() ([]Session, error) { return nil, errUnsupported }

// DefaultPaths returns no paths off Linux, where utmp is unavailable.
func DefaultPaths() []string { return nil }

// SessionsFrom reports that utmp is unavailable on non-Linux platforms.
func SessionsFrom([]string) ([]Session, error) { return nil, errUnsupported }
