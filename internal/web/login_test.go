package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"sermo/internal/logfile"
)

type loginHarness struct {
	h     http.Handler
	clock time.Time
}

// newLoginHarness serves auth on a clock the test moves; configure adjusts the
// server before its handler is built.
func newLoginHarness(auth Auth, configure ...func(*Server)) *loginHarness {
	lh := &loginHarness{clock: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	server := &Server{
		Backend:  StaticBackend{Backend: &fakeBackend{services: []Service{{Name: "web"}}}},
		Auth:     auth,
		Hostname: "k2keu3",
		now:      func() time.Time { return lh.clock },
	}
	for _, c := range configure {
		c(server)
	}
	lh.h = server.Handler()
	return lh
}

func (lh *loginHarness) do(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	lh.h.ServeHTTP(rec, r)
	return rec
}

func loginPost(password string) *http.Request {
	form := url.Values{loginFieldPassword: {password}}
	r := httptest.NewRequest(http.MethodPost, routePathLogin, strings.NewReader(form.Encode()))
	r.Header.Set(headerContentType, "application/x-www-form-urlencoded")
	r.Header.Set(headerOrigin, "http://"+r.Host)
	return r
}

func sessionCookieFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if strings.HasPrefix(c.Name, sessionCookiePrefix) {
			return c
		}
	}
	return nil
}

func withSession(r *http.Request, c *http.Cookie) *http.Request {
	r.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
	return r
}

func TestLoginPageIsAPasswordOnlyForm(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")})
	rec := lh.do(httptest.NewRequest(http.MethodGet, routePathLogin, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /login = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`type="password"`, `autocomplete="current-password"`, `method="post"`, `action="login"`,
		"k2keu3",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("login page missing %q", want)
		}
	}
	if strings.Contains(body, "login/basic") {
		t.Error("login page still offers the removed browser password dialog")
	}
	// One credential field: no username for a password manager to stumble on.
	if strings.Count(body, "<input") != 1 {
		t.Errorf("login page has %d inputs, want only the password", strings.Count(body, "<input"))
	}
	// The style block carries this response's CSP nonce.
	csp := rec.Header().Get(headerContentSecurityPolicy)
	if !strings.Contains(body, `<style nonce="`) || strings.Contains(body, `nonce=""`) || !strings.Contains(csp, "nonce-") {
		t.Errorf("login page style is not bound to the CSP nonce: csp=%q", csp)
	}
	if rec.Header().Get(headerWWWAuthenticate) != "" {
		t.Error("the form must not summon the browser dialog")
	}
	// no-referrer would make the browser post the form with `Origin: null`,
	// which the origin check refuses: the login page must keep a real Origin.
	if got := rec.Header().Get(headerReferrerPolicy); got != headerValueSameOrigin {
		t.Errorf("Referrer-Policy = %q, want %q so the form posts its Origin", got, headerValueSameOrigin)
	}
}

func TestLoginStartsAnAdminSession(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret"), SessionTTL: time.Hour})
	rec := lh.do(loginPost("secret"))
	if rec.Code != http.StatusSeeOther || landing(rec, routePathLogin) != routePathRoot {
		t.Fatalf("login = %d loc=%q, want 303 to the dashboard", rec.Code, rec.Header().Get("Location"))
	}
	c := sessionCookieFrom(rec)
	if c == nil {
		t.Fatal("login set no session cookie")
	}
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.MaxAge != 3600 || c.Path != routePathRoot || c.Secure || c.Name != sessionCookiePrefix+"_k2keu3" {
		t.Fatalf("cookie = %+v, want %s_k2keu3, HttpOnly, Lax, Max-Age 3600, Path /, not Secure over plain HTTP", c, sessionCookiePrefix)
	}

	who := lh.do(withSession(httptest.NewRequest(http.MethodGet, apiPathWhoami, nil), c))
	if !strings.Contains(who.Body.String(), `"role":"admin"`) {
		t.Fatalf("whoami with session = %s, want admin", who.Body.String())
	}
	// The session acts like the password: an action with the CSRF header works.
	act := lh.do(withSession(req(http.MethodPost, testServicePath("web", apiActionRestart), "", ""), c))
	if act.Code != http.StatusOK {
		t.Fatalf("action with session = %d, want 200", act.Code)
	}
	// ...and the CSRF header is still required with a cookie.
	forged := withSession(httptest.NewRequest(http.MethodPost, testServicePath("web", apiActionRestart), nil), c)
	if rec := lh.do(forged); rec.Code != http.StatusForbidden {
		t.Fatalf("cookie action without CSRF header = %d, want 403", rec.Code)
	}
	// An admin who opens /login again goes straight home.
	if rec := lh.do(withSession(httptest.NewRequest(http.MethodGet, routePathLogin, nil), c)); rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /login with admin session = %d, want 303", rec.Code)
	}
}

func TestLoginGuestPasswordGivesReadOnlySession(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret"), GuestCredentials: testCredentials(t, "guestpw")})
	c := sessionCookieFrom(lh.do(loginPost("guestpw")))
	if c == nil {
		t.Fatal("guest login set no session cookie")
	}
	if rec := lh.do(withSession(httptest.NewRequest(http.MethodGet, APIPathServices, nil), c)); rec.Code != http.StatusOK {
		t.Fatalf("guest read = %d, want 200", rec.Code)
	}
	if rec := lh.do(withSession(req(http.MethodPost, testServicePath("web", apiActionRestart), "", ""), c)); rec.Code != http.StatusForbidden {
		t.Fatalf("guest action = %d, want 403", rec.Code)
	}
}

func TestLoginWrongPasswordAndThrottling(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")})
	for i := range loginMaxFailures {
		rec := lh.do(loginPost("nope"))
		if rec.Code != http.StatusUnauthorized || sessionCookieFrom(rec) != nil {
			t.Fatalf("attempt %d = %d, want 401 without a cookie", i+1, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), loginMessageInvalid) {
			t.Fatalf("attempt %d page lacks the error message", i+1)
		}
	}
	// Locked out: even the right password waits for the window to end.
	rec := lh.do(loginPost("secret"))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get(headerRetryAfter) == "" || sessionCookieFrom(rec) != nil {
		t.Fatalf("locked attempt = %d retry=%q, want 429 with Retry-After", rec.Code, rec.Header().Get(headerRetryAfter))
	}
	lh.clock = lh.clock.Add(loginFailureWindow)
	if rec := lh.do(loginPost("secret")); rec.Code != http.StatusSeeOther {
		t.Fatalf("login after the window = %d, want 303", rec.Code)
	}
}

func TestLoginRejectsCrossSiteForm(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret"), PublicURL: "https://sermo.example.com/ops"})
	for name, set := range map[string]func(*http.Request){
		"foreign origin": func(r *http.Request) { r.Header.Set(headerOrigin, "https://evil.example") },
		"opaque origin":  func(r *http.Request) { r.Header.Set(headerOrigin, originOpaque) },
		"cross-site fetch": func(r *http.Request) {
			r.Header.Set(headerSecFetchSite, "cross-site")
		},
	} {
		t.Run(name, func(t *testing.T) {
			r := loginPost("secret")
			set(r)
			if rec := lh.do(r); rec.Code != http.StatusForbidden || sessionCookieFrom(rec) != nil {
				t.Fatalf("cross-site login = %d, want 403 without a cookie", rec.Code)
			}
		})
	}
	// The public URL's host is this dashboard too (a proxy may rewrite Host),
	// and so is the proxy's X-Forwarded-Host.
	r := loginPost("secret")
	r.Header.Set(headerOrigin, "https://sermo.example.com")
	if rec := lh.do(r); rec.Code != http.StatusSeeOther {
		t.Fatalf("login via public host = %d, want 303", rec.Code)
	}
	r = loginPost("secret")
	r.Header.Set(headerOrigin, "https://proxy.example.net")
	r.Header.Set(headerXForwardedHost, "proxy.example.net")
	if rec := lh.do(r); rec.Code != http.StatusSeeOther {
		t.Fatalf("login via X-Forwarded-Host = %d, want 303", rec.Code)
	}
	// The browser's own same-origin verdict wins over a Host the proxy rewrote.
	r = loginPost("secret")
	r.Header.Set(headerOrigin, "https://elsewhere.example.org")
	r.Header.Set(headerSecFetchSite, secFetchSiteSame)
	if rec := lh.do(r); rec.Code != http.StatusSeeOther {
		t.Fatalf("same-origin per Sec-Fetch-Site = %d, want 303", rec.Code)
	}
}

func TestSessionExpiresAndLogoutEndsIt(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret"), SessionTTL: time.Hour})
	c := sessionCookieFrom(lh.do(loginPost("secret")))
	whoami := func() *httptest.ResponseRecorder {
		return lh.do(withSession(httptest.NewRequest(http.MethodGet, apiPathWhoami, nil), c))
	}
	lh.clock = lh.clock.Add(59 * time.Minute)
	if rec := whoami(); rec.Code != http.StatusOK {
		t.Fatalf("session before expiry = %d, want 200", rec.Code)
	}
	lh.clock = lh.clock.Add(time.Minute)
	if rec := whoami(); rec.Code != http.StatusUnauthorized {
		t.Fatalf("expired session = %d, want 401", rec.Code)
	}

	c = sessionCookieFrom(lh.do(loginPost("secret")))
	// Logout needs the dashboard's CSRF header, like every state change.
	if rec := lh.do(withSession(httptest.NewRequest(http.MethodPost, routePathLogout, nil), c)); rec.Code != http.StatusForbidden {
		t.Fatalf("logout without CSRF header = %d, want 403", rec.Code)
	}
	rec := lh.do(withSession(req(http.MethodPost, routePathLogout, "", ""), c))
	if cleared := sessionCookieFrom(rec); rec.Code != http.StatusOK || cleared == nil || cleared.MaxAge >= 0 {
		t.Fatalf("logout = %d cookie=%+v, want 200 and a deleted cookie", rec.Code, cleared)
	}
	if rec := whoami(); rec.Code != http.StatusUnauthorized {
		t.Fatalf("session after logout = %d, want 401", rec.Code)
	}
}

// Basic auth stays for API clients (sermoctl, curl) alongside the form.
func TestBasicAuthStillWorksBesideSessions(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret"), RuntimeToken: "run-token"})
	for _, pass := range []string{"secret", "run-token"} {
		if rec := lh.do(req(http.MethodPost, testServicePath("web", apiActionRestart), "sermoctl", pass)); rec.Code != http.StatusOK {
			t.Fatalf("Basic %q action = %d, want 200", pass, rec.Code)
		}
	}
}

// Open mode has nothing to log in to.
func TestLoginInOpenModeGoesHome(t *testing.T) {
	lh := newLoginHarness(Auth{})
	if rec := lh.do(httptest.NewRequest(http.MethodGet, routePathLogin, nil)); rec.Code != http.StatusSeeOther {
		t.Fatalf("GET /login in open mode = %d, want 303", rec.Code)
	}
}

func TestSessionStoreBoundsItsSize(t *testing.T) {
	now := time.Now()
	store := newSessionStore(time.Hour)
	first, err := store.create(roleGuest, now)
	if err != nil {
		t.Fatal(err)
	}
	for range maxSessions {
		now = now.Add(time.Second)
		if _, err := store.create(roleGuest, now); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.sessions) != maxSessions || store.role(first, now) != "" {
		t.Fatalf("sessions = %d, first role = %q; want the oldest dropped at the cap", len(store.sessions), store.role(first, now))
	}
}

// The login page's only script carries the response's CSP nonce, and nothing on
// it uses inline event handlers.
func TestLoginPageCSPHygiene(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")})
	doc, err := html.Parse(lh.do(httptest.NewRequest(http.MethodGet, routePathLogin, nil)).Body)
	if err != nil {
		t.Fatal(err)
	}
	walk(doc, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if n.DataAtom == atom.Script {
			nonce := ""
			for _, a := range n.Attr {
				if a.Key == "nonce" {
					nonce = a.Val
				}
			}
			if nonce == "" {
				t.Error("login page <script> has no CSP nonce")
			}
		}
		for _, a := range n.Attr {
			if strings.HasPrefix(a.Key, "on") {
				t.Errorf("inline event handler %q on <%s>", a.Key, n.Data)
			}
		}
	})
}

// Logging in is an operator action the audit trail keeps, success and failure
// alike, without the password.
func TestAccessLogRecordsLogins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	log, err := logfile.Open(path)
	if err != nil {
		t.Fatalf("logfile.Open: %v", err)
	}
	defer log.Close()
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")}, func(s *Server) { s.AccessLog = log })

	lh.do(loginPost("wrong-guess"))
	lh.do(loginPost("secret"))

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "secret") || strings.Contains(string(data), "wrong-guess") {
		t.Fatalf("access log leaks a password: %s", data)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("access log = %q, want two login records", data)
	}
	want := []struct {
		actor  string
		status float64
	}{{accessActorAnonymous, http.StatusUnauthorized}, {roleAdmin, http.StatusSeeOther}}
	for i, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		if entry[accessFieldPath] != routePathLogin || entry[accessFieldActor] != want[i].actor || entry[accessFieldStatus] != want[i].status {
			t.Fatalf("record %d = %v, want %s %v on %s", i, entry, want[i].actor, want[i].status, routePathLogin)
		}
	}
}

// landing resolves a redirect the way a browser does, against the path of the
// request that received it.
func landing(rec *httptest.ResponseRecorder, requestPath string) string {
	loc, err := url.Parse(rec.Header().Get("Location"))
	if err != nil {
		return ""
	}
	return (&url.URL{Path: requestPath}).ResolveReference(loc).Path
}

// The session cookie must work however the dashboard is reached: Secure only
// when the request came over https, so an operator opening host:9797 directly
// (or through an SSH tunnel) can log in even with an https public_url.
func TestSessionCookieSecureFollowsTheRequest(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret"), PublicURL: "https://sermo.example.com/ops"})
	direct := lh.do(loginPost("secret"))
	if c := sessionCookieFrom(direct); c == nil || c.Secure || c.Path != routePathRoot {
		t.Fatalf("direct login cookie = %+v, want not Secure, Path /", c)
	}
	if landing(direct, routePathLogin) != routePathRoot {
		t.Fatalf("direct login lands on %q, want /", landing(direct, routePathLogin))
	}
	for name, set := range map[string]func(*http.Request){
		"public host":       func(r *http.Request) { r.Host = "sermo.example.com" },
		"X-Forwarded-Proto": func(r *http.Request) { r.Header.Set(headerXForwardedProto, "https") },
	} {
		t.Run(name, func(t *testing.T) {
			r := loginPost("secret")
			set(r)
			r.Header.Set(headerOrigin, "https://sermo.example.com")
			if c := sessionCookieFrom(lh.do(r)); c == nil || !c.Secure {
				t.Fatalf("cookie = %+v, want Secure", c)
			}
		})
	}
}

// Cookies are scoped by host name, not port: two dashboards reached on one
// host name must keep separate logins.
func TestSessionCookieNameCarriesTheHost(t *testing.T) {
	for host, want := range map[string]string{
		"k2keu3":    sessionCookiePrefix + "_k2keu3",
		"fr5.lan":   sessionCookiePrefix + "_fr5.lan",
		`bad;"name`: sessionCookiePrefix + "_badname",
		"":          sessionCookiePrefix,
	} {
		if got := sessionCookieName(host); got != want {
			t.Errorf("sessionCookieName(%q) = %q, want %q", host, got, want)
		}
	}
}

// A burst of concurrent guesses cannot exceed the limit: each attempt counts
// before its password check runs.
func TestLoginLimiterCountsAttemptsUpFront(t *testing.T) {
	l := newLoginLimiter()
	now := time.Now()
	for i := range loginMaxFailures {
		if _, ok := l.reserve("198.51.100.7", now); !ok {
			t.Fatalf("attempt %d refused, want allowed", i+1)
		}
	}
	wait, ok := l.reserve("198.51.100.7", now)
	if ok || wait <= 0 {
		t.Fatalf("attempt %d = ok %v wait %v, want refused", loginMaxFailures+1, ok, wait)
	}
	if retryAfterSeconds(time.Millisecond) != "1" {
		t.Fatalf("Retry-After for 1ms = %q, want 1 (never 0)", retryAfterSeconds(time.Millisecond))
	}
}

// Behind a reverse proxy on this host every browser shares the proxy's
// address; the limit must follow the client the proxy names instead, while a
// remote peer cannot pick its own key.
func TestClientAddressBehindLocalProxy(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, routePathLogin, nil)
	r.RemoteAddr = "127.0.0.1:41000"
	r.Header.Set(headerXForwardedFor, "10.9.9.9, 203.0.113.5")
	if got := clientAddress(r); got != "203.0.113.5" {
		t.Fatalf("behind local proxy = %q, want the proxy-appended client", got)
	}
	r.RemoteAddr = "198.51.100.7:5555"
	if got := clientAddress(r); got != "198.51.100.7" {
		t.Fatalf("remote peer = %q, want its own address (X-Forwarded-For ignored)", got)
	}
}

// Basic auth on the API shares the form's limit, so it is not a way around it.
func TestBasicAuthGuessesAreThrottled(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")})
	for range loginMaxFailures {
		lh.do(req(http.MethodGet, apiPathWhoami, "x", "guess"))
	}
	if rec := lh.do(req(http.MethodGet, apiPathWhoami, "x", "secret")); rec.Code != http.StatusUnauthorized {
		t.Fatalf("right password after the limit = %d, want 401 until the window ends", rec.Code)
	}
	if rec := lh.do(loginPost("secret")); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("form after Basic guesses = %d, want 429 (one limit)", rec.Code)
	}
}

// Only a /login session can be ended by the server, so whoami tells the
// dashboard when to offer "log out"; a new login replaces the old session.
func TestWhoamiReportsSessionAndReloginReplacesIt(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")})
	if body := lh.do(req(http.MethodGet, apiPathWhoami, "x", "secret")).Body.String(); !strings.Contains(body, `"session":false`) {
		t.Fatalf("whoami over Basic = %s, want session false", body)
	}
	first := sessionCookieFrom(lh.do(loginPost("secret")))
	if body := lh.do(withSession(httptest.NewRequest(http.MethodGet, apiPathWhoami, nil), first)).Body.String(); !strings.Contains(body, `"session":true`) {
		t.Fatalf("whoami with session = %s, want session true", body)
	}
	second := sessionCookieFrom(lh.do(withSession(loginPost("secret"), first)))
	if second == nil || second.Value == first.Value {
		t.Fatal("re-login issued no new session")
	}
	if rec := lh.do(withSession(httptest.NewRequest(http.MethodGet, apiPathWhoami, nil), first)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("old session after re-login = %d, want 401", rec.Code)
	}
}

// A full store evicts within the new login's role: guest logins cannot push
// out admin sessions.
func TestSessionStoreEvictsWithinTheRole(t *testing.T) {
	now := time.Now()
	store := newSessionStore(time.Hour)
	admin, err := store.create(roleAdmin, now)
	if err != nil {
		t.Fatal(err)
	}
	for range maxSessions + 10 {
		now = now.Add(time.Second)
		if _, err := store.create(roleGuest, now); err != nil {
			t.Fatal(err)
		}
	}
	if store.role(admin, now) != roleAdmin {
		t.Fatal("guest logins evicted the admin session")
	}
}

// The login routes are ordinary mux routes: their relaxed policy covers only the
// method and path the mux matched. Another method is an ordinary request — an
// admin gets the mux's 405, an anonymous caller is refused before routing — and
// an escaped path the mux sends elsewhere is not exempt either.
func TestLoginRoutesAreMuxRoutes(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")})
	for _, tc := range []struct {
		method, path, allow string
	}{
		{http.MethodPut, routePathLogin, "POST"},
		{http.MethodDelete, routePathLogout, "POST"},
	} {
		if rec := lh.do(req(tc.method, tc.path, "", "")); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s %s = %d, want 401", tc.method, tc.path, rec.Code)
		}
		rec := lh.do(req(tc.method, tc.path, "admin", "secret"))
		if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Header().Get("Allow"), tc.allow) {
			t.Errorf("%s %s = %d allow=%q, want 405 allowing %s", tc.method, tc.path, rec.Code, rec.Header().Get("Allow"), tc.allow)
		}
	}
}

func TestLoginRouteAccessBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
		csrf   bool
		want   int
	}{
		{name: "form read", method: http.MethodGet, path: routePathLogin, want: http.StatusOK},
		{name: "form head", method: http.MethodHead, path: routePathLogin, want: http.StatusOK},
		{name: "escaped login segment", method: http.MethodGet, path: "/log%69n", want: http.StatusOK},
		{name: "form submit with query", method: http.MethodPost, path: "/login?next=/", want: http.StatusSeeOther},
		{name: "logout without session", method: http.MethodPost, path: routePathLogout, csrf: true, want: http.StatusOK},
		{name: "logout needs CSRF", method: http.MethodPost, path: routePathLogout, want: http.StatusForbidden},
		{name: "logout GET is not public", method: http.MethodGet, path: routePathLogout, want: http.StatusUnauthorized},
		{name: "form PUT is not public", method: http.MethodPut, path: routePathLogin, csrf: true, want: http.StatusUnauthorized},
		{name: "form suffix is not public", method: http.MethodGet, path: "/login/extra", want: http.StatusUnauthorized},
		{name: "retired route is not public", method: http.MethodGet, path: "/login/basic", want: http.StatusUnauthorized},
		{name: "encoded slash is not public", method: http.MethodGet, path: "/login%2Fbasic", want: http.StatusUnauthorized},
		{name: "retired submit needs CSRF", method: http.MethodPost, path: "/login/basic", want: http.StatusForbidden},
		{name: "retired submit needs auth", method: http.MethodPost, path: "/login/basic", csrf: true, want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")})
			form := url.Values{loginFieldPassword: {"secret"}}
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(form.Encode()))
			r.Header.Set(headerContentType, "application/x-www-form-urlencoded")
			r.Header.Set(headerOrigin, "http://"+r.Host)
			if tc.csrf {
				r.Header.Set(HeaderCSRF, "1")
			}
			rec := lh.do(r)
			if rec.Code != tc.want || rec.Header().Get(headerWWWAuthenticate) != "" {
				t.Fatalf("%s %s = %d challenge=%q, want %d without a challenge", tc.method, tc.path, rec.Code, rec.Header().Get(headerWWWAuthenticate), tc.want)
			}
			if cookie := sessionCookieFrom(rec); cookie != nil && cookie.MaxAge > 0 && tc.want != http.StatusSeeOther {
				t.Fatalf("%s %s unexpectedly issued a session", tc.method, tc.path)
			}
		})
	}
}

// web.login_message shows under the host as escaped plain text: the page is
// served to anyone, so it never carries markup.
func TestLoginPageShowsTheLoginMessage(t *testing.T) {
	lh := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")}, func(s *Server) { s.LoginMessage = "Producción <b>prod</b>" })
	body := lh.do(httptest.NewRequest(http.MethodGet, routePathLogin, nil)).Body.String()
	if !strings.Contains(body, `<p class="login-note">Producción &lt;b&gt;prod&lt;/b&gt;</p>`) {
		t.Fatalf("login page lacks the escaped note:\n%s", body)
	}
	plain := newLoginHarness(Auth{AdminCredentials: testCredentials(t, "secret")})
	if strings.Contains(plain.do(httptest.NewRequest(http.MethodGet, routePathLogin, nil)).Body.String(), `<p class="login-note">`) {
		t.Fatal("login page shows a note although none is configured")
	}
}
