package servicemgr

import (
	"fmt"
	"strings"
)

// Backend identifies a supported service manager backend.
type Backend string

const (
	BackendAuto    Backend = "auto"
	BackendSystemd Backend = "systemd"
	BackendOpenRC  Backend = "openrc"
)

// ParseBackend parses a backend name used by CLI flags and environment values.
func ParseBackend(value string) (Backend, error) {
	switch Backend(strings.TrimSpace(strings.ToLower(value))) {
	case "", BackendAuto:
		return BackendAuto, nil
	case BackendSystemd:
		return BackendSystemd, nil
	case BackendOpenRC:
		return BackendOpenRC, nil
	default:
		return "", fmt.Errorf("unknown backend %q (expected auto, systemd or openrc)", value)
	}
}

// Status is the normalized service status returned by managers.
type Status string

const (
	StatusActive   Status = "active"
	StatusInactive Status = "inactive"
	StatusFailed   Status = "failed"
	StatusUnknown  Status = "unknown"
)
