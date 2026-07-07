package conn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	ldap "github.com/go-ldap/ldap/v3"
)

func init() { Register(ldapProtocol{}) }

const (
	defaultLDAPPort         = 389
	defaultLDAPProbeTimeout = 5 * time.Second
)

// ldapProtocol probes an LDAP directory using go-ldap. With no user it performs
// an anonymous bind; with a user/password it performs a simple bind (the user is
// the bind DN). TLS is implicit (LDAPS) when enabled — use port 636.
type ldapProtocol struct{}

func (ldapProtocol) Name() string       { return "ldap" }
func (ldapProtocol) DefaultPort() int   { return defaultLDAPPort }
func (ldapProtocol) RequiresUser() bool { return false }

func (ldapProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultLDAPPort
	}
	timeout := defaultLDAPProbeTimeout
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			timeout = d
		}
	}

	url, useTLS := buildLDAPURL(host, port, cfg.TLS)
	opts := []ldap.DialOpt{ldap.DialWithDialer(probeDialer(cfg.Interface, timeout))}
	if useTLS {
		tc := tlsClientConfig(host)
		if normalizeTLS(cfg.TLS) == tlsSkipVerify {
			tc.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		opts = append(opts, ldap.DialWithTLSConfig(tc))
	}

	l, err := ldap.DialURL(url, opts...)
	if err != nil {
		return Result{}, err
	}
	defer l.Close()
	l.SetTimeout(timeout)

	requireAuth := cfg.User != ""
	mode := "anonymous"
	var bindErr error
	if requireAuth {
		mode = "simple"
		bindErr = l.Bind(cfg.User, cfg.Password)
	} else {
		_, bindErr = l.SimpleBind(&ldap.SimpleBindRequest{AllowEmptyPassword: true})
	}

	bindOK := bindErr == nil
	serverResponded := bindOK
	if !bindOK {
		var lerr *ldap.Error
		if errors.As(bindErr, &lerr) && lerr.ResultCode != ldap.ErrorNetwork {
			serverResponded = true // the server replied with an LDAP result code
		}
	}
	if !ldapSucceeds(bindOK, serverResponded, requireAuth) {
		return Result{}, fmt.Errorf("ldap bind: %w", bindErr)
	}

	extra := map[string]string{"bind": mode}
	if bindOK {
		extra["result"] = "success"
	} else {
		extra["result"] = bindErr.Error() // anonymous: server up but bind rejected
	}
	return Result{Extra: extra}, nil
}

// buildLDAPURL builds the dial URL and reports whether TLS (LDAPS) is used.
func buildLDAPURL(host string, port int, tlsMode string) (url string, useTLS bool) {
	useTLS = normalizeTLS(tlsMode) != ""
	scheme := "ldap"
	if useTLS {
		scheme = "ldaps"
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)), useTLS
}

// ldapSucceeds decides the outcome: an anonymous check passes if the server
// responded at all (a successful bind or an LDAP-level rejection — both prove it
// is up and speaking LDAP, only a transport error fails); a credentialed check
// requires the bind to succeed.
func ldapSucceeds(bindOK, serverResponded, requireAuth bool) bool {
	if requireAuth {
		return bindOK
	}
	return serverResponded
}
