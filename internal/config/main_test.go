package config

import (
	"os"
	"testing"
)

// TestMain pins the host facts this package reads at load time, so the tests
// behave the same on a systemd or an OpenRC host and under whatever SERMO_*
// overrides the shell exports: the backend and init overrides are cleared,
// ${init} is systemd, and an empty PATH turns any host command a test reaches
// by accident (systemctl list-units, rc-status) into a failure to start rather
// than a read of this machine's service manager.
func TestMain(m *testing.M) {
	for _, name := range []string{EnvBackendOverride, envInitOverride} {
		_ = os.Unsetenv(name)
	}
	detectedInit = backendSystemd
	_ = os.Setenv("PATH", "")
	os.Exit(m.Run())
}
