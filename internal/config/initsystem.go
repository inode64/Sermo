package config

import (
	"os"
	"strings"
)

// detectedInit holds the init system used as the ${init} built-in
// (systemd | openrc). Resolved once at package load; SERMO_INIT overrides
// detection (handy off-host or in tests).
var detectedInit = detectInit()

func detectInit() string {
	if v := strings.TrimSpace(os.Getenv("SERMO_INIT")); v != "" {
		return strings.ToLower(v)
	}
	if _, err := os.Stat("/run/systemd/system"); err == nil {
		return "systemd"
	}
	if _, err := os.Stat("/run/openrc"); err == nil {
		return "openrc"
	}
	if _, err := os.Stat("/sbin/openrc"); err == nil {
		return "openrc"
	}
	return "systemd"
}
