//go:build !linux

package notify

import (
	"errors"
)

func buildTTY(name string, entry map[string]any) (Notifier, error) {
	return nil, errors.New("tty notifier is only supported on Linux")
}

func buildWall(name string, entry map[string]any) (Notifier, error) {
	return nil, errors.New("wall notifier is only supported on Linux")
}

// ActiveUserCount is Linux-only; elsewhere there is no utmp to read.
func ActiveUserCount() int { return 0 }
