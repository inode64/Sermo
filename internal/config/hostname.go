package config

import (
	"os"
	"strings"
)

// detectedHostname holds the short hostname used for the ${hostname} built-in.
// Resolved once at package load; tests may override it before calling Load.
//
// Unlike ${host} (a bind-address fallback that keeps the full os.Hostname()),
// ${hostname} is the *short* hostname — the first label before the first dot.
// systemd instance units keyed by host identity use the short form: a Ceph
// monitor on radon.srvdr.com runs as `ceph-mon@radon`, not `ceph-mon@radon.srvdr.com`.
// That is why a daemon writes `service: "ceph-mon@${hostname}"`.
var detectedHostname = detectHostname()

func detectHostname() string {
	// SERMO_HOSTNAME is taken verbatim (like SERMO_HOST), so an operator can
	// force any instance id, including a full FQDN if their units need it.
	if v := envOverride("SERMO_HOSTNAME"); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		short, _, _ := strings.Cut(h, ".")
		if short != "" {
			return short
		}
	}
	return "localhost"
}
