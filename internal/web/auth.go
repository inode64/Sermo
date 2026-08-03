package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"

	"sermo/internal/webcred"
)

// hostLocalname is the hostname (and suffix, `*.localhost`) that always
// resolves to loopback per RFC 6761.
const hostLocalname = "localhost"

// Auth controls access to the dashboard via HTTP Basic auth with two roles:
//
//   - admin: full access (read and actions). Granted by AdminCredentials.
//   - guest: read-only (GET/HEAD only; state-changing requests are refused). Granted by
//     GuestCredentials, or to anonymous requests when AnonymousGuest is set.
//
// When no field is set, auth is disabled and every request is treated as admin
// (the UI is open) — suitable only behind a trusted boundary.
//
// The password (not the username) determines the role: enter any username and any
// of the passwords configured for that role. Passwords are compared in constant
// time; see internal/webcred for the credential formats.
type Auth struct {
	AdminCredentials webcred.List
	GuestCredentials webcred.List
	AnonymousGuest   bool

	// RuntimeToken is the daemon-generated admin credential sermoctl reads from
	// <paths.runtime>/web.token. It exists because hashed credentials leave no
	// password for the CLI to send. Empty disables it.
	RuntimeToken string
}

// String redacts the runtime token, which is an admin credential in its own
// right and must not reach a log line through a formatted Auth or Server.
func (a Auth) String() string {
	return fmt.Sprintf("web.Auth{admin: %v, guest: %v, anonymous_guest: %v, runtime_token: %v}",
		a.AdminCredentials, a.GuestCredentials, a.AnonymousGuest, a.RuntimeToken != "")
}

// Enabled reports whether any access control is configured. The runtime token is
// not access control by itself: it is generated only alongside a configured
// credential, and it must never turn the open dashboard into a closed one.
func (a Auth) Enabled() bool {
	return !a.AdminCredentials.Empty() || !a.GuestCredentials.Empty() || a.AnonymousGuest
}

// Role values returned by role() and surfaced in the whoami response. The empty
// string means unauthenticated.
const (
	roleAdmin = "admin"
	roleGuest = "guest"
)

const (
	whoamiFieldAuth   = "auth"
	whoamiFieldCanAct = "can_act"
	whoamiFieldRole   = "role"
)

const (
	authMessageMissingCSRFHeader = "missing " + headerSermoCSRF + " header (CSRF protection)"
	authMessageReadOnly          = "read-only access"
	authMessageRequired          = "authentication required"
	authMessageForeignHost       = "request Host does not name this server (DNS-rebinding protection); add it to web.allowed_hosts if legitimate"
)

// hostAllowed reports whether the request Host names this server: a loopback
// name/address, the configured bind host, or an explicit AllowedHosts entry.
// Ports are ignored — rebinding controls the name, not the port.
func (s *Server) hostAllowed(hostport string) bool {
	host := canonicalHost(hostport)
	if host == "" {
		return false
	}
	if host == hostLocalname || strings.HasSuffix(host, "."+hostLocalname) {
		return true
	}
	// Any IP-literal Host is direct addressing, not rebinding: a rebound
	// request necessarily carries the attacker's DNS name in Host.
	if ip := net.ParseIP(host); ip != nil {
		return true
	}
	if bind := canonicalHost(s.Addr); bind != "" && host == bind {
		return true
	}
	return slices.ContainsFunc(s.AllowedHosts, func(allowed string) bool {
		return canonicalHost(allowed) == host
	})
}

// canonicalHost lowercases and strips an optional port and IPv6 brackets.
func canonicalHost(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		hostport = h
	}
	return strings.ToLower(strings.Trim(hostport, "[]"))
}

// role resolves a request to roleAdmin, roleGuest, or "" (unauthenticated).
func (a Auth) role(r *http.Request) string {
	if !a.Enabled() {
		return roleAdmin
	}
	if _, pass, ok := r.BasicAuth(); ok {
		// The token is checked first: it is the cheap comparison, and it is what
		// sermoctl sends on every call.
		if a.RuntimeToken != "" && webcred.SecureEqual(pass, a.RuntimeToken) {
			return roleAdmin
		}
		ctx := r.Context()
		if a.AdminCredentials.Verify(ctx, pass) {
			return roleAdmin
		}
		if a.GuestCredentials.Verify(ctx, pass) {
			return roleGuest
		}
	}
	if a.AnonymousGuest {
		return roleGuest
	}
	return ""
}

type roleCtxKey struct{}

func roleFrom(ctx context.Context) string {
	role, _ := ctx.Value(roleCtxKey{}).(string)
	return role
}

// withAuth enforces the role on each request: unauthenticated requests get a Basic
// challenge, guests may only read (GET/HEAD), and /login is an admin-only
// endpoint that triggers the browser's login prompt then redirects home (used to
// escalate from anonymous guest to admin).
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Plain liveness/readiness probes are public: monitors and load balancers
		// carry no credentials. Verbose probes include inventory/runtime details,
		// so they follow normal read auth when auth is enabled.
		if isPlainHealthProbe(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Open mode has no credential boundary, so a DNS-rebound page could
		// drive the API from a hostile origin; only Hosts that name this server
		// are served. With auth enabled the Basic credential check covers it (a
		// rebound origin cannot attach credentials) and proxies keep their Host.
		if !s.Auth.Enabled() && s.Addr != "" && !s.hostAllowed(r.Host) {
			writeJSON(w, http.StatusMisdirectedRequest, ActionResult{OK: false, Message: authMessageForeignHost})
			return
		}

		role := s.Auth.role(r)

		if r.URL.Path == routePathLogin {
			if role == roleAdmin {
				http.Redirect(w, r, routePathRoot, http.StatusSeeOther)
			} else {
				s.challenge(w)
			}
			return
		}
		// CSRF: state-changing requests must carry the custom header (set by the
		// dashboard's fetch). Checked before auth so a forged cross-site request is
		// rejected even when the browser would attach cached credentials.
		if !isReadMethod(r.Method) && r.Header.Get(headerSermoCSRF) == "" {
			writeJSON(w, http.StatusForbidden, ActionResult{OK: false, Message: authMessageMissingCSRFHeader})
			return
		}
		if role == "" {
			s.denyUnauthenticated(w, r)
			return
		}
		if !isReadMethod(r.Method) && role != roleAdmin {
			writeJSON(w, http.StatusForbidden, ActionResult{OK: false, Message: authMessageReadOnly})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), roleCtxKey{}, role)))
	})
}

func (s *Server) challenge(w http.ResponseWriter) {
	w.Header().Set(headerWWWAuthenticate, authBasicRealmSermo)
	writeJSON(w, http.StatusUnauthorized, ActionResult{OK: false, Message: authMessageRequired})
}

// denyUnauthenticated refuses a request that carries no usable credential, and
// decides whether the browser should be asked for one.
//
// WWW-Authenticate on a subresource is what made the dashboard re-prompt on its
// own. The page holds an EventSource on /api/stream, and the server tells it to
// reconnect every 5s; a reconnect that arrives without the cached credential
// used to get the challenge back, which browsers answer with a modal password
// box even though the user was doing nothing. Several dashboards open at once
// multiply the reconnections and so the prompts.
//
// The challenge belongs on a document navigation, and on /login, which exists
// precisely to summon the dialog and bounce back home. Everything else — API
// calls, the stream — gets a plain 401 that the dashboard handles itself.
func (s *Server) denyUnauthenticated(w http.ResponseWriter, r *http.Request) {
	if wantsAuthDialog(r) {
		s.challenge(w)
		return
	}
	writeJSON(w, http.StatusUnauthorized, ActionResult{OK: false, Message: authMessageRequired})
}

// wantsAuthDialog reports whether a 401 for r should carry the Basic challenge.
// Sec-Fetch-Mode is authoritative where the browser sends it: "navigate" is a
// document load, while fetch and EventSource report cors/same-origin/no-cors.
// Older clients fall back to Accept, where a document request asks for HTML and
// the API asks for JSON or text/event-stream.
func wantsAuthDialog(r *http.Request) bool {
	// The two document routes always challenge, whatever the client sends: they
	// are how a person reaches the dashboard, and the first login must work for
	// a client that sets neither header.
	if r.URL.Path == routePathRoot || r.URL.Path == routePathLogin {
		return true
	}
	if mode := r.Header.Get(headerSecFetchMode); mode != "" {
		return mode == secFetchModeNavigate
	}
	return strings.Contains(r.Header.Get(headerAccept), contentTypeHTML)
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	// withAuth resolves the role before any handler runs; an empty role can only
	// mean the middleware was bypassed, so fail closed to read-only, not admin.
	role := roleFrom(r.Context())
	if role == "" {
		role = roleGuest
	}
	writeJSON(w, http.StatusOK, map[string]any{
		whoamiFieldRole:   role,
		whoamiFieldCanAct: role == roleAdmin,
		whoamiFieldAuth:   s.Auth.Enabled(),
	})
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func isPlainHealthProbe(r *http.Request) bool {
	if r == nil || !isReadMethod(r.Method) || r.URL.Query().Has(apiQueryVerbose) {
		return false
	}
	return r.URL.Path == routePathLivez || r.URL.Path == routePathReadyz
}
