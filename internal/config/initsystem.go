package config

import (
	"os"
	"strings"
)

const (
	initSystemdRuntimeDir = "/run/systemd/system"
	initOpenRCRuntimeDir  = "/run/openrc"
	initOpenRCBinaryPath  = "/sbin/openrc"
)

// detectedInit holds the init system used as the ${init} built-in
// (systemd | openrc). Resolved once at package load; SERMO_INIT overrides
// detection (handy off-host or in tests).
var detectedInit = detectInit()

func detectInit() string {
	if v := envOverride(envInitOverride); v != "" {
		return strings.ToLower(v)
	}
	if _, err := os.Stat(initSystemdRuntimeDir); err == nil {
		return backendSystemd
	}
	if _, err := os.Stat(initOpenRCRuntimeDir); err == nil {
		return backendOpenRC
	}
	if _, err := os.Stat(initOpenRCBinaryPath); err == nil {
		return backendOpenRC
	}
	return backendSystemd
}
