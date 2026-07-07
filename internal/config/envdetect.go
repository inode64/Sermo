package config

import (
	"os"
	"strings"
)

// EnvBackendOverride is the environment variable that selects the service
// manager backend for validation and CLI defaults.
const EnvBackendOverride = "SERMO_BACKEND"

const (
	envHostOverride     = "SERMO_HOST"
	envHostnameOverride = "SERMO_HOSTNAME"
	envUserOverride     = "SERMO_USER"
	envInitOverride     = "SERMO_INIT"
	envArchOverride     = "SERMO_ARCH"
	envOSOverride       = "SERMO_OS"
)

// envOverride returns the trimmed value of one of the SERMO_* detector or
// rendering overrides (SERMO_HOST, SERMO_HOSTNAME, SERMO_USER, SERMO_INIT,
// SERMO_BACKEND, SERMO_ARCH, SERMO_OS), or "" when unset. Every built-in
// detector honors its override so configuration can be rendered and validated
// off-host (see the built-in variable table in docs/services.md).
func envOverride(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}
