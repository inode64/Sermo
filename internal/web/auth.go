package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"sermo/internal/httpx"
	"sermo/internal/webcred"
)

// hostLocalname is the hostname (and suffix, `*.localhost`) that always
// resolves to loopback per RFC 6761.
const hostLocalname = "localhost"

// Auth controls access to the dashboard with two roles:
//
//   - admin: full access (read and actions). Granted by AdminCredentials.
//   - guest: read-only (GET/HEAD only; state-changing requests are refused). Granted by
//     GuestCredentials, or to anonymous requests when AnonymousGuest is set.
//
// When no field is set, auth is disabled and every request is treated as admin
// (the UI is open) — suitable only behind a trusted boundary.
//
// The password alone determines the role. Browsers present it once on the
// /login form (a single password field password managers fill) and then carry
// a session cookie; API clients such as sermoctl send it on every request as
// HTTP Basic auth, where any username is accepted. Passwords are compared in
// constant time; see internal/webcred for the credential formats.
type Auth struct {
	AdminCredentials webcred.List
	GuestCredentials webcred.List
	AnonymousGuest   bool

	// RuntimeToken is the daemon-generated admin credential sermoctl reads from
	// <paths.runtime>/web.token. It exists because hashed credentials leave no
	// password for the CLI to send. Empty disables it.
	RuntimeToken string

	// SessionTTL is how long a /login session lasts; zero uses
	// defaultSessionTTL.
	SessionTTL time.Duration
	// PublicURL is web.public_url. Sermo serves no TLS itself, so an https
	// public URL is what marks the session cookie Secure; its path scopes the
	// cookie and the login redirects behind a reverse proxy.
	PublicURL string
}

// String redacts the runtime token, which is an admin credential in its own
// right and must not reach a log line through a formatted Auth or Server.
func (a Auth) String() string {
	return fmt.Sprintf("web.Auth{admin: %v, guest: %v, anonymous_guest: %v, runtime_token: %v, session_ttl: %v}",
		a.AdminCredentials, a.GuestCredentials, a.AnonymousGuest, a.RuntimeToken != "", a.sessionTTL())
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
	whoamiFieldAuth    = "auth"
	whoamiFieldCanAct  = "can_act"
	whoamiFieldRole    = "role"
	whoamiFieldSession = "session"
)

const (
	authMessageMissingCSRFHeader = "missing " + HeaderCSRF + " header (CSRF protection)"
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

// role resolves a request to roleAdmin, roleGuest, or "" (unauthenticated): an
// explicit Basic credential first, then the /login session cookie, then the
// anonymous guest fallback. session reports that the role came from the
// cookie, the only login a browser can end with /logout.
func (s *Server) role(r *http.Request) (role string, session bool) {
	if !s.Auth.Enabled() {
		return roleAdmin, false
	}
	if role := s.basicRole(r); role != "" {
		return role, false
	}
	if c, err := r.Cookie(s.cookieName); err == nil {
		if role := s.sessions.role(c.Value, s.now()); role != "" {
			return role, true
		}
	}
	if s.Auth.AnonymousGuest {
		return roleGuest, false
	}
	return "", false
}

// basicRole checks an HTTP Basic password under the same per-client limit as
// the login form, so the API is not a way around it. A client that has used up
// its attempts gets no password check at all until its window ends; a session
// cookie it also carries still works.
func (s *Server) basicRole(r *http.Request) string {
	_, pass, ok := r.BasicAuth()
	if !ok {
		return ""
	}
	client := clientAddress(r)
	if _, allowed := s.loginLimiter.reserve(client, s.now()); !allowed {
		return ""
	}
	role := s.Auth.passwordRole(r.Context(), pass)
	if role != "" {
		s.loginLimiter.succeed(client)
	}
	return role
}

// initLoginState prepares what the login routes and role resolution share:
// the session store, the attempt limiter, the parsed public URL and the cookie
// name. It runs once, when the auth middleware is built.
func (s *Server) initLoginState() {
	if s.now == nil {
		s.now = time.Now
	}
	if s.sessions == nil {
		s.sessions = newSessionStore(s.Auth.sessionTTL())
	}
	if s.loginLimiter == nil {
		s.loginLimiter = newLoginLimiter()
	}
	s.site = newPublicSite(s.Auth.PublicURL)
	s.cookieName = sessionCookieName(s.Hostname)
}

// passwordRole maps a presented password to the role it grants, or "".
func (a Auth) passwordRole(ctx context.Context, pass string) string {
	// The token is checked first: it is the cheap comparison, and it is what
	// sermoctl sends on every call.
	if a.RuntimeToken != "" && webcred.SecureEqual(pass, a.RuntimeToken) {
		return roleAdmin
	}
	if a.AdminCredentials.Verify(ctx, pass) {
		return roleAdmin
	}
	if a.GuestCredentials.Verify(ctx, pass) {
		return roleGuest
	}
	return ""
}

func (a Auth) sessionTTL() time.Duration {
	if a.SessionTTL > 0 {
		return a.SessionTTL
	}
	return defaultSessionTTL
}

type roleCtxKey struct{}

type sessionCtxKey struct{}

func roleFrom(ctx context.Context) string {
	role, _ := ctx.Value(roleCtxKey{}).(string)
	return role
}

// sessionFrom reports whether the request's role came from a /login session.
func sessionFrom(ctx context.Context) bool {
	session, _ := ctx.Value(sessionCtxKey{}).(bool)
	return session
}

// authRoute relaxes the default access policy for the routes through which a
// role is obtained or dropped. It is keyed by the mux pattern the request
// matched — method included, matched the way the mux matches it — so it applies
// to exactly the handler it names: an escaped path or another method that the
// mux sends elsewhere never inherits it.
type authRoute struct {
	// anyRole serves the route whatever the caller's role, including none:
	// it is how someone without a role gets one, or a guest drops theirs.
	anyRole bool
	// formPost exempts the route from the X-Sermo-Csrf header, which a plain
	// HTML form cannot send; the handler checks the form's origin instead.
	formPost bool
}

var authRoutes = map[string]authRoute{
	routeLoginForm:   {anyRole: true},
	routeLoginSubmit: {anyRole: true, formPost: true},
	routeLoginBasic:  {anyRole: true},
	routeLogout:      {anyRole: true},
}

// routePolicy is the relaxed policy of the mux route r will reach, if any.
func routePolicy(next http.Handler, r *http.Request) authRoute {
	mux, ok := next.(*http.ServeMux)
	if !ok {
		return authRoute{}
	}
	_, pattern := mux.Handler(r)
	return authRoutes[pattern]
}

// withAuth enforces the role on each request: an unauthenticated page load is
// sent to the /login form, other unauthenticated requests get a plain 401, and
// guests may only read (GET/HEAD). The routes in authRoutes relax that policy;
// every handler finds the resolved role in its context (roleFrom).
func (s *Server) withAuth(next http.Handler) http.Handler {
	s.initLoginState()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Plain liveness/readiness probes are public: monitors and load balancers
		// carry no credentials. Verbose probes include inventory/runtime details,
		// so they follow normal read auth when auth is enabled.
		if isPlainHealthProbe(r) {
			next.ServeHTTP(w, r)
			return
		}
		role, session := s.role(r)
		publishAccessActor(r.Context(), role)

		// Open mode has no credential boundary, so a DNS-rebound page could
		// drive the API from a hostile origin; only Hosts that name this server
		// are served. With auth enabled the credential check covers it (a
		// rebound origin cannot attach Basic credentials or the host-scoped
		// session cookie) and proxies keep their Host.
		if !s.Auth.Enabled() && s.Addr != "" && !s.hostAllowed(r.Host) {
			writeJSON(w, http.StatusMisdirectedRequest, ActionResult{OK: false, Message: authMessageForeignHost})
			return
		}

		policy := routePolicy(next, r)
		// CSRF: state-changing requests must carry the custom header (set by the
		// dashboard's fetch). Checked before auth so a forged cross-site request is
		// rejected even when the browser would attach cached credentials.
		if !isReadMethod(r.Method) && !policy.formPost && r.Header.Get(HeaderCSRF) == "" {
			writeJSON(w, http.StatusForbidden, ActionResult{OK: false, Message: authMessageMissingCSRFHeader})
			return
		}
		ctx := context.WithValue(context.WithValue(r.Context(), roleCtxKey{}, role), sessionCtxKey{}, session)
		if policy.anyRole {
			next.ServeHTTP(w, r.WithContext(ctx))
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
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) challenge(w http.ResponseWriter) {
	w.Header().Set(headerWWWAuthenticate, basicAuthChallenge(s.Hostname))
	writeJSON(w, http.StatusUnauthorized, ActionResult{OK: false, Message: authMessageRequired})
}

// basicAuthRealmValue is the unquoted realm string: "Sermo" or "Sermo <host>".
func basicAuthRealmValue(shortHost string) string {
	shortHost = strings.TrimSpace(shortHost)
	if shortHost == "" {
		return authBasicRealmPrefix
	}
	return authBasicRealmPrefix + " " + shortHost
}

// basicAuthChallenge builds the WWW-Authenticate value for a Basic challenge.
// The realm includes the short hostname when known so Chrome's password manager
// labels each dashboard distinctly across many open tabs.
func basicAuthChallenge(shortHost string) string {
	return `Basic realm="` + escapeHTTPQuotedString(basicAuthRealmValue(shortHost)) + `"`
}

// escapeHTTPQuotedString escapes \ and " for an RFC 7230 quoted-string.
func escapeHTTPQuotedString(s string) string {
	if !strings.ContainsAny(s, `\"`) {
		return s
	}
	// Worst case every rune is escaped: double the length is enough headroom.
	const escapeHeadroom = 2
	var b strings.Builder
	b.Grow(len(s) * escapeHeadroom)
	for i := range len(s) {
		switch s[i] {
		case '\\', '"':
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
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
// A document navigation is sent to the /login form instead, and only
// /login/basic, which exists precisely to summon the browser dialog, ever
// challenges. Everything else — API calls, the stream — gets a plain 401 that
// the dashboard handles itself.
func (*Server) denyUnauthenticated(w http.ResponseWriter, r *http.Request) {
	if isPageLoad(r) {
		redirectWithin(w, r, routePathLogin[1:])
		return
	}
	writeJSON(w, http.StatusUnauthorized, ActionResult{OK: false, Message: authMessageRequired})
}

// isPageLoad reports whether an unauthenticated request is a person loading a
// page, who should be sent to the login form, rather than an API client.
// Sec-Fetch-Mode is authoritative where the browser sends it: "navigate" is a
// document load, while fetch and EventSource report cors/same-origin/no-cors.
// Older clients fall back to Accept, where a document request asks for HTML and
// the API asks for JSON or text/event-stream.
func isPageLoad(r *http.Request) bool {
	// The dashboard root always counts as a page load, whatever the client
	// sends: it is how a person reaches the dashboard, and the first login must
	// work for a client that sets neither header.
	if r.URL.Path == routePathRoot {
		return true
	}
	if mode := r.Header.Get(headerSecFetchMode); mode != "" {
		return mode == secFetchModeNavigate
	}
	return strings.Contains(r.Header.Get(httpx.HeaderAccept), contentTypeHTML)
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
		// The dashboard offers "log out" only for a /login session: a browser's
		// cached Basic credential cannot be dropped by the server.
		whoamiFieldSession: sessionFrom(r.Context()),
	})
}

func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead
}

func isPlainHealthProbe(r *http.Request) bool {
	if !isReadMethod(r.Method) || r.URL.Query().Has(apiQueryVerbose) {
		return false
	}
	return r.URL.Path == routePathLivez || r.URL.Path == routePathReadyz
}
