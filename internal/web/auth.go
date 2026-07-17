package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"net"
	"net/http"
	"slices"
	"strings"
)

// hostLocalname is the hostname (and suffix, `*.localhost`) that always
// resolves to loopback per RFC 6761.
const hostLocalname = "localhost"

// Auth controls access to the dashboard via HTTP Basic auth with two roles:
//
//   - admin: full access (read and actions). Granted by AdminPassword.
//   - guest: read-only (GET/HEAD only; state-changing requests are refused). Granted by
//     GuestPassword, or to anonymous requests when AnonymousGuest is set.
//
// When no field is set, auth is disabled and every request is treated as admin
// (the UI is open) — suitable only behind a trusted boundary.
//
// The password (not the username) determines the role: enter any username and the
// admin or guest password. Passwords are compared in constant time.
type Auth struct {
	AdminPassword  string
	GuestPassword  string
	AnonymousGuest bool
}

// Enabled reports whether any access control is configured.
func (a Auth) Enabled() bool {
	return a.AdminPassword != "" || a.GuestPassword != "" || a.AnonymousGuest
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
		if a.AdminPassword != "" && secureEqual(pass, a.AdminPassword) {
			return roleAdmin
		}
		if a.GuestPassword != "" && secureEqual(pass, a.GuestPassword) {
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
			s.challenge(w)
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

func secureEqual(a, b string) bool {
	ah := sha256.Sum256([]byte(a))
	bh := sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(ah[:], bh[:]) == 1
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
