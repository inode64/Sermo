package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

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

// WebPublicURL returns web.public_url without a trailing slash: the dashboard
// address notifications link to, or "" when unset or not a usable URL.
func (g Global) WebPublicURL() string {
	raw, present := g.WebSection()[WebKeyPublicURL]
	if !present || validatePublicURL(raw) != nil {
		return ""
	}
	return strings.TrimRight(cfgval.AsString(raw), "/")
}

// WebSessionTTL returns web.session_ttl, or 0 when unset or invalid
// (validation reports an invalid value) so the web server applies its default.
func (g Global) WebSessionTTL() time.Duration {
	if ttl, ok := cfgval.ParseDuration(g.WebSection()[WebKeySessionTTL]); ok && ttl > 0 {
		return ttl
	}
	return 0
}

// maxLoginMessageRunes bounds web.login_message: one short line under the host
// name on the login page, not a banner.
const maxLoginMessageRunes = 200

// WebLoginMessage returns web.login_message trimmed, or "" when unset or
// invalid (validation reports an invalid value).
func (g Global) WebLoginMessage() string {
	raw, present := g.WebSection()[WebKeyLoginMessage]
	if !present || validateLoginMessage(raw) != nil {
		return ""
	}
	return strings.TrimSpace(cfgval.AsString(raw))
}

// validateLoginMessage accepts a single line of at most maxLoginMessageRunes
// characters. It is plain text: the login page escapes it.
func validateLoginMessage(raw any) error {
	s, isStr := raw.(string)
	if !isStr {
		return errors.New("must be a string")
	}
	if strings.ContainsAny(s, "\r\n") {
		return errors.New("must be a single line")
	}
	if utf8.RuneCountInString(s) > maxLoginMessageRunes {
		return fmt.Errorf("must be at most %d characters", maxLoginMessageRunes)
	}
	return nil
}

// validatePublicURL accepts an absolute http(s) URL: the dashboard root,
// optionally under a reverse-proxy path. A fragment is Sermo's to add.
func validatePublicURL(raw any) error {
	s, isStr := raw.(string)
	if !isStr {
		return errors.New("must be a string")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("must be an absolute http:// or https:// URL")
	}
	if u.Fragment != "" || u.RawQuery != "" {
		return errors.New("must not carry a query or fragment")
	}
	return nil
}
