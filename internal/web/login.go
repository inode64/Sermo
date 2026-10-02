package web

import (
	"bytes"
	"context"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"sermo/internal/buildinfo"
	"sermo/internal/netutil"
)

const (
	assetLoginHTML      = "login.html"
	loginFieldPassword  = "password"
	loginFormMaxBytes   = 4 << 10
	secFetchSiteSame    = "same-origin"
	secFetchSiteNone    = "none"
	originOpaque        = "null"
	loginMessageInvalid = "Wrong password."
	loginMessageLimited = "Too many failed attempts. Try again later."
	loginMessageOrigin  = "login form posted from another site"
	logMsgLogin         = "web login"
	logFieldRole        = "role"
	logFieldClient      = "client"
	logFieldResult      = "result"
	loginResultOK       = "ok"
	loginResultFailed   = "failed"
	loginResultLimited  = "rate_limited"
)

// loginPage is the standalone /login document: one password field, which is
// what lets a password manager fill and save it.
var loginPage = template.Must(template.ParseFS(assets, assetLoginHTML))

type loginPageData struct {
	Nonce   string
	Host    string
	Note    string // web.login_message, under the host name
	Version string
	Message string // the outcome of a failed attempt
}

// publicSite is what web.public_url says about how browsers reach the
// dashboard, parsed once when the handler is built: its host is a valid login
// form origin when a proxy rewrites Host, and https there marks the session
// cookie Secure for requests that arrive through it.
type publicSite struct {
	host   string
	secure bool
}

// newPublicSite reads web.public_url, which config has already validated.
func newPublicSite(raw string) publicSite {
	u, err := url.Parse(raw)
	if raw == "" || err != nil {
		return publicSite{}
	}
	return publicSite{host: u.Host, secure: u.Scheme == netutil.URLSchemeHTTPS}
}

// redirectWithin answers 303 to target, a path relative to the dashboard root,
// expressed relative to the request (see relativePath). It sets Location itself
// because http.Redirect would make the URL absolute against the path Sermo
// sees, dropping the reverse-proxy prefix the browser is under.
func redirectWithin(w http.ResponseWriter, r *http.Request, target string) {
	w.Header().Set(headerLocation, relativePath(r, target))
	w.WriteHeader(http.StatusSeeOther)
}

// relativePath turns target, a path relative to the dashboard root ("" for the
// root itself, "login" for the form), into a URL relative to the request's own
// path. Redirects then land on the dashboard however the browser reached it:
// directly on host:port, through an SSH tunnel, or under a reverse-proxy path
// that Sermo never sees.
func relativePath(r *http.Request, target string) string {
	depth := strings.Count(strings.TrimPrefix(r.URL.Path, "/"), "/")
	prefix := strings.Repeat("../", depth)
	if prefix == "" {
		prefix = "./"
	}
	return prefix + target
}

// handleLoginForm serves the password form; an admin, or an open dashboard,
// has nothing to log in to and goes home.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.Enabled() || roleFrom(r.Context()) == roleAdmin {
		redirectWithin(w, r, "")
		return
	}
	s.renderLogin(w, r, http.StatusOK, "")
}

// handleLoginBasic summons the browser's own password dialog, kept as an
// alternative to the form: it challenges until the browser sends an admin
// credential, then goes home.
func (s *Server) handleLoginBasic(w http.ResponseWriter, r *http.Request) {
	if roleFrom(r.Context()) == roleAdmin {
		redirectWithin(w, r, "")
		return
	}
	s.challenge(w)
}

// handleLoginSubmit checks the form's password. A correct one starts a session
// cookie carrying the role it grants.
func (s *Server) handleLoginSubmit(w http.ResponseWriter, r *http.Request) {
	if !s.Auth.Enabled() {
		redirectWithin(w, r, "")
		return
	}
	// A plain HTML form cannot carry the X-Sermo-Csrf header, so the form's
	// own origin is the cross-site check: a page elsewhere must not be able to
	// log a browser into this dashboard.
	if !s.sameOriginForm(r) {
		writeJSON(w, http.StatusForbidden, ActionResult{OK: false, Message: loginMessageOrigin})
		return
	}
	client := clientAddress(r)
	if wait, ok := s.loginLimiter.reserve(client, s.now()); !ok {
		s.logLogin(r.Context(), client, "", loginResultLimited)
		w.Header().Set(headerRetryAfter, retryAfterSeconds(wait))
		s.renderLogin(w, r, http.StatusTooManyRequests, loginMessageLimited)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, loginFormMaxBytes)
	role := s.Auth.passwordRole(r.Context(), r.PostFormValue(loginFieldPassword))
	if role == "" {
		s.logLogin(r.Context(), client, "", loginResultFailed)
		s.renderLogin(w, r, http.StatusUnauthorized, loginMessageInvalid)
		return
	}
	s.loginLimiter.succeed(client)
	token, err := s.sessions.create(role, s.now())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, ActionResult{OK: false, Message: "cannot start a session: " + err.Error()})
		return
	}
	// A new login replaces this browser's previous session rather than
	// leaving it valid for the rest of its lifetime.
	if old, err := r.Cookie(s.cookieName); err == nil {
		s.sessions.delete(old.Value)
	}
	publishAccessActor(r.Context(), role)
	s.logLogin(r.Context(), client, role, loginResultOK)
	http.SetCookie(w, s.sessionCookie(r, token, int(s.Auth.sessionTTL().Seconds())))
	// The form posts to "login" plus the page's #fragment, which a browser
	// keeps across this redirect: a notification's deep link to a row survives
	// the detour through the form.
	redirectWithin(w, r, "")
}

// retryAfterSeconds renders a lockout's remaining time for Retry-After, rounded
// up so a client never reads 0 and retries straight into another refusal.
func retryAfterSeconds(wait time.Duration) string {
	return strconv.Itoa(max(int((wait+time.Second-1)/time.Second), 1))
}

// clientAddress is the key the login limiter counts attempts against: the
// peer's address, or — when the peer is a reverse proxy on this host, the only
// supported way to expose the dashboard — the client address that proxy
// appended to X-Forwarded-For. Without that, every browser behind the proxy
// would share one bucket, and five bad guesses from anyone would lock every
// operator out. A remote peer's X-Forwarded-For is ignored: anyone can send it.
func clientAddress(r *http.Request) string {
	peer := canonicalHost(r.RemoteAddr)
	if ip := net.ParseIP(peer); ip == nil || !ip.IsLoopback() {
		return peer
	}
	forwarded := r.Header.Values(headerXForwardedFor)
	if len(forwarded) == 0 {
		return peer
	}
	hops := strings.Split(forwarded[len(forwarded)-1], ",")
	if last := strings.TrimSpace(hops[len(hops)-1]); last != "" {
		return canonicalHost(last)
	}
	return peer
}

// handleLogout ends the browser's session. withAuth has already required the
// dashboard's X-Sermo-Csrf header, so another site cannot log you out.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(s.cookieName); err == nil {
		s.sessions.delete(c.Value)
	}
	http.SetCookie(w, s.sessionCookie(r, "", -1))
	writeJSON(w, http.StatusOK, ActionResult{OK: true, Message: "logged out"})
}

// sessionCookie builds the session cookie; maxAge -1 deletes it. Lax, not
// Strict: a dashboard link opened from a notification is a cross-site
// navigation, and Strict would bounce an operator who is logged in back to the
// form. Cross-site POSTs still never carry it, and the API's CSRF header
// covers state changes regardless.
//
// Secure cannot be unconditional: Sermo serves plain HTTP (TLS belongs to a
// reverse proxy), and a Secure cookie would never come back over plain HTTP —
// an operator who opens host:9797 directly could never log in. It is set when
// this request reached Sermo over https (see viaHTTPS). The path is "/": the
// cookie is already scoped to this host, and a reverse-proxy prefix is a path
// Sermo never sees.
func (s *Server) sessionCookie(r *http.Request, token string, maxAge int) *http.Cookie {
	return &http.Cookie{ //nolint:gosec // G124: Secure follows the request's scheme; see above.
		Name:     s.cookieName,
		Value:    token,
		Path:     routePathRoot,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.viaHTTPS(r),
		SameSite: http.SameSiteLaxMode,
	}
}

// viaHTTPS reports whether the browser reached this request over https: the
// proxy said so (X-Forwarded-Proto), or the request names web.public_url's
// host and that URL is https. Trusting the header is safe here: at worst a
// client marks its own cookie Secure.
func (s *Server) viaHTTPS(r *http.Request) bool {
	if strings.EqualFold(r.Header.Get(headerXForwardedProto), netutil.URLSchemeHTTPS) {
		return true
	}
	return s.site.secure && strings.EqualFold(r.Host, s.site.host)
}

// sessionCookieName is the cookie name for a server identified by hostname:
// the prefix plus the host identity, reduced to cookie-token characters.
func sessionCookieName(hostname string) string {
	var b strings.Builder
	for _, c := range hostname {
		if c < 0x80 && (c == '-' || c == '_' || c == '.' || ('0' <= c && c <= '9') || ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')) {
			b.WriteRune(c)
		}
	}
	if b.Len() == 0 {
		return sessionCookiePrefix
	}
	return sessionCookiePrefix + "_" + b.String()
}

// sameOriginForm accepts a form the browser says was posted from this
// dashboard. Sec-Fetch-Site is the browser's own verdict and survives a proxy
// that rewrites Host, so it decides when present. Otherwise the Origin host
// must name this server: the request Host, the proxy's X-Forwarded-Host, or
// web.public_url's host. A client that sends neither header is not a browser,
// and the CSRF concern does not apply to it.
func (s *Server) sameOriginForm(r *http.Request) bool {
	if site := r.Header.Get(headerSecFetchSite); site != "" {
		return site == secFetchSiteSame || site == secFetchSiteNone
	}
	origin := r.Header.Get(headerOrigin)
	if origin == "" {
		return true
	}
	if origin == originOpaque {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	for _, host := range []string{r.Host, r.Header.Get(headerXForwardedHost), s.site.host} {
		if host != "" && strings.EqualFold(u.Host, host) {
			return true
		}
	}
	return false
}

func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, status int, message string) {
	var page bytes.Buffer
	err := loginPage.Execute(&page, loginPageData{
		Nonce:   cspNonceFrom(r.Context()),
		Host:    s.Hostname,
		Note:    s.LoginMessage,
		Version: buildinfo.Short(),
		Message: message,
	})
	if err != nil {
		http.Error(w, "login page unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set(headerContentType, contentTypeHTMLUTF8)
	w.Header().Set(headerCacheControl, headerValueNoStore)
	// The dashboard-wide no-referrer policy makes a browser send `Origin: null`
	// on this page's form POST, which sameOriginForm must refuse — and over
	// plain HTTP there is no Sec-Fetch-Site to fall back on. same-origin keeps
	// the real Origin on the post to this server while still sending nothing
	// to any other site.
	w.Header().Set(headerReferrerPolicy, headerValueSameOrigin)
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(page.Bytes())
	}
}

// logLogin records every attempt in the daemon log, which unlike the optional
// access log (engine.access) is always on and carries the client address the
// rate limit keys on.
func (s *Server) logLogin(ctx context.Context, client, role, result string) {
	if s.Logger == nil {
		return
	}
	level := slog.LevelInfo
	if result != loginResultOK {
		level = slog.LevelWarn
	}
	s.Logger.Log(ctx, level, logMsgLogin, logFieldClient, client, logFieldRole, role, logFieldResult, result)
}
