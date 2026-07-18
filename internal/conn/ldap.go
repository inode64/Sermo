package conn

import (
	"context"
	"errors"
	"fmt"
	"time"

	ldap "github.com/go-ldap/ldap/v3"

	"sermo/internal/netutil"
)

func init() { Register(ldapProtocol{}) }

const (
	defaultLDAPPort         = defaultPortLDAP
	defaultLDAPProbeTimeout = 5 * time.Second
	ldapBindAnonymous       = "anonymous"
	ldapBindSimple          = "simple"
	ldapResultSuccess       = "success"
)

// ldapProtocol probes an LDAP directory using go-ldap. With no user it performs
// an anonymous bind; with a user/password it performs a simple bind (the user is
// the bind DN). TLS is implicit (LDAPS) when enabled — use port 636.
type ldapProtocol struct{}

func (ldapProtocol) Name() string       { return ProtocolNameLDAP }
func (ldapProtocol) DefaultPort() int   { return defaultLDAPPort }
func (ldapProtocol) RequiresUser() bool { return false }

func (ldapProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host, port := cfg.hostPortDefaults(defaultLDAPPort)
	timeout := netutil.TimeoutFromContext(ctx, defaultLDAPProbeTimeout)

	url, useTLS := buildLDAPURL(host, port, cfg.TLS)
	opts := []ldap.DialOpt{ldap.DialWithDialer(probeDialer(cfg.Interface, timeout))}
	if useTLS {
		tc := tlsClientConfig(host)
		if NormalizeTLS(cfg.TLS) == tlsSkipVerify {
			tc.InsecureSkipVerify = true // operator chose tls: skip-verify
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
	mode := ldapBindAnonymous
	var bindErr error
	if requireAuth {
		mode = ldapBindSimple
		bindErr = l.Bind(cfg.User, cfg.Password)
	} else {
		_, bindErr = l.SimpleBind(&ldap.SimpleBindRequest{AllowEmptyPassword: true})
	}

	bindOK := bindErr == nil
	serverResponded := bindOK
	if !bindOK {
		if lerr, ok := errors.AsType[*ldap.Error](bindErr); ok && lerr.ResultCode != ldap.ErrorNetwork {
			serverResponded = true // the server replied with an LDAP result code
		}
	}
	if !ldapSucceeds(bindOK, serverResponded, requireAuth) {
		return Result{}, fmt.Errorf("ldap bind: %w", bindErr)
	}

	extra := map[string]string{extraBind: mode}
	if bindOK {
		extra[extraResult] = ldapResultSuccess
	} else {
		extra[extraResult] = bindErr.Error() // anonymous: server up but bind rejected
	}
	return Result{Extra: extra}, nil
}

// buildLDAPURL builds the dial URL and reports whether TLS (LDAPS) is used.
func buildLDAPURL(host string, port int, tlsMode string) (url string, useTLS bool) {
	useTLS = NormalizeTLS(tlsMode) != ""
	scheme := "ldap"
	if useTLS {
		scheme = "ldaps"
	}
	return scheme + urlSchemeSeparator + hostPort(host, port), useTLS
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
