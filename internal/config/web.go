package config

import (
	"errors"
	"fmt"

	"sermo/internal/cfgval"
	"sermo/internal/netutil"
)

var (
	// ErrWebNotConfigured reports that the global config has no web section.
	ErrWebNotConfigured = errors.New("no [web] section in config")
	// ErrWebPortUnset reports that the web section does not select a port.
	ErrWebPortUnset = errors.New("web.port is not set")
)

// WebBind is the validated host and port shared by the web listener and its
// local API clients.
type WebBind struct {
	Host string
	Port int
}

// HostPort formats the bind for listeners and URLs, including IPv6 brackets.
func (b WebBind) HostPort() string {
	return netutil.JoinHostPort(b.Host, b.Port)
}

// WebBind resolves and validates the configured web listener endpoint.
func (g Global) WebBind() (WebBind, error) {
	web := g.WebSection()
	if web == nil {
		return WebBind{}, ErrWebNotConfigured
	}
	rawPort, present := web[WebKeyPort]
	if !present {
		return WebBind{}, ErrWebPortUnset
	}
	port, ok := cfgval.Int(rawPort)
	if !ok {
		return WebBind{}, fmt.Errorf("web.port is not a number (%T)", rawPort)
	}
	if !cfgval.ValidTCPPort(port) {
		return WebBind{}, fmt.Errorf("web.port must be in %s (got %d)", cfgval.TCPPortRange(), port)
	}

	host := netutil.LoopbackIPv4
	if rawHost, present := web[WebKeyAddress]; present {
		address, ok := rawHost.(string)
		if !ok {
			return WebBind{}, fmt.Errorf("web.address must be a string (got %T)", rawHost)
		}
		if address != "" {
			host = address
		}
	}
	return WebBind{Host: host, Port: port}, nil
}
