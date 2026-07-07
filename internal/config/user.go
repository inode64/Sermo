package config

import (
	"os/user"
)

// detectedUser holds the username used as the ${user} fallback (the user running
// Sermo). Resolved once at package load; SERMO_USER overrides it. Like ${host} it
// is not baked: a catalog service's own `user` variable (a service account such as
// www-data) always wins, and the built-in only applies when none is declared.
var detectedUser = detectUser()

func detectUser() string {
	if v := envOverride(envUserOverride); v != "" {
		return v
	}
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return "root"
}
