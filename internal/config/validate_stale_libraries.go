package config

import (
	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

func validateStaleLibrariesCheck(path string, entry map[string]any, add addFunc) {
	for _, key := range []string{checks.CheckKeyExe, checks.CheckKeyExeAny, checks.CheckKeyUser, checks.CheckKeyState, checks.CheckKeyScope, checks.CheckKeyPaths} {
		if _, present := entry[key]; present {
			add("%s.%s is not accepted; stale_libraries uses the owning service's processes or a read-only host inventory", path, key)
		}
	}
	if raw, present := entry[checks.CheckKeyStableFor]; present && cfgval.Duration(raw) <= 0 {
		add("%s.stable_for must be a positive duration", path)
	}
}
