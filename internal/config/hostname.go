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
// monitor on node1.example.com runs as `ceph-mon@node1`, not `ceph-mon@node1.example.com`.
// That is why a catalog service writes `service: "ceph-mon@${hostname}"`.
// The web UI reuses the same value for the Basic auth realm and the browser tab
// title so multi-host operators see one consistent host name.
var detectedHostname = detectHostname()

// ShortHostname returns the short host identity used for ${hostname}, the web
// Basic realm and the dashboard tab title. SERMO_HOSTNAME overrides it
// verbatim; otherwise the first label of os.Hostname() is used.
func ShortHostname() string {
	return detectedHostname
}

func detectHostname() string {
	// SERMO_HOSTNAME is taken verbatim (like SERMO_HOST), so an operator can
	// force any instance id, including a full FQDN if their units need it.
	if v := envOverride(envHostnameOverride); v != "" {
		return v
	}
	if h, err := os.Hostname(); err == nil && h != "" {
		short, _, _ := strings.Cut(h, ".")
		if short != "" {
			return short
		}
	}
	return defaultLocalHostname
}
