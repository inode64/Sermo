package config

import (
	"os"
)

// detectedHost holds the hostname used as the ${host} fallback. Resolved once at
// package load; tests may override it before calling Load. Unlike ${arch}/${os}
// it is not baked, because `host` is a common user-defined variable (a bind
// address); the built-in only applies when the daemon does not define one.
var detectedHost = detectHost()

func detectHost() string {
	if v := envOverride("SERMO_HOST"); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "localhost"
}
