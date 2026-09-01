package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"sermo/internal/webcred"
)

func authServer(a Auth) http.Handler {
	return (&Server{Backend: &fakeBackend{services: []Service{{Name: "web"}}}, Auth: a}).Handler()
}

func req(method, path, user, pass string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	if !isReadMethod(method) {
		r.Header.Set(headerSermoCSRF, "1")
	}
	if user != "" || pass != "" {
		r.SetBasicAuth(user, pass)
	}
	return r
}

func TestAuthStringRedactsRuntimeToken(t *testing.T) {
	const token = "runtime-admin-secret"
	auth := Auth{
		AdminCredentials: testCredentials(t, "configured-admin-secret"),
		RuntimeToken:     token,
	}
	for _, rendered := range []string{
		auth.String(),
		fmt.Sprintf("%v", auth),
		fmt.Sprintf("%+v", auth),
	} {
		if strings.Contains(rendered, token) {
			t.Errorf("rendered Auth = %q, want the runtime token redacted", rendered)
		}
		if !strings.Contains(rendered, "runtime_token: true") {
			t.Errorf("rendered Auth = %q, want token presence without its value", rendered)
		}
	}
}

type fakeReadiness struct{ rep ReadyReport }

func (f fakeReadiness) Report(_ context.Context) ReadyReport { return f.rep }

func TestLivezPublicEvenWithAuth(t *testing.T) {
	// auth required for everything else, but /livez must answer without credentials
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePathLivez, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/livez = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok\n" {
		t.Fatalf("/livez body = %q, want \"ok\\n\"", got)
	}
	// a normal endpoint still challenges
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, apiPathServices, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/services without auth = %d, want 401", rec.Code)
	}
}

func TestReadyzPublicEvenWithAuth(t *testing.T) {
	h := (&Server{
		Backend:   &fakeBackend{services: []Service{{Name: "web"}}},
		Auth:      Auth{AdminCredentials: testCredentials(t, "secret")},
		Readiness: fakeReadiness{rep: ReadyReport{Ready: true, Status: apiStatusOK, Services: 1}},
	}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePathReadyz, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != apiStatusOKLine {
		t.Fatalf("/readyz body = %q", got)
	}
}

func TestVerboseHealthRequiresAuth(t *testing.T) {
	h := (&Server{
		Backend:   &fakeBackend{services: []Service{{Name: "web"}}},
		Auth:      Auth{AdminCredentials: testCredentials(t, "secret")},
		Readiness: fakeReadiness{rep: ReadyReport{Ready: true, Status: apiStatusOK, Services: 1}},
	}).Handler()
	for _, path := range []string{
		testFlagQuery(routePathLivez),
		testFlagQuery(routePathReadyz),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("GET %s without auth = %d, want 401", path, rec.Code)
		}

		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, req(http.MethodGet, path, "admin", "secret"))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s with admin auth = %d, want 200", path, rec.Code)
		}
	}
}

func TestReadyzStartingReturns503(t *testing.T) {
	h := (&Server{
		Backend: &fakeBackend{services: []Service{{Name: "web"}}},
		Readiness: fakeReadiness{rep: ReadyReport{
			Status: "starting", Message: "monitoring has not started yet", Services: 1,
		}},
	}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testFlagQuery(routePathReadyz), nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz starting = %d, want 503", rec.Code)
	}
	var got ReadyReport
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Ready || got.Status != "starting" {
		t.Fatalf("report = %+v", got)
	}
}

func TestLivezVerbose(t *testing.T) {
	h := authServer(Auth{}) // open
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, testFlagQuery(routePathLivez), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/livez?verbose = %d, want 200", rec.Code)
	}
	var got struct {
		Status   string `json:"status"`
		Uptime   string `json:"uptime"`
		Services int    `json:"services"`
		Go       string `json:"go"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Status != apiStatusOK || got.Uptime == "" || got.Services != 1 || got.Go == "" {
		t.Fatalf("unexpected livez verbose: %+v", got)
	}
}

func TestCSRFGuardOnUnsafeMethods(t *testing.T) {
	h := authServer(Auth{}) // open mode: even without auth, a forged request is blocked
	// no CSRF header -> rejected
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, testServicePath("web", apiActionRestart), nil)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF header = %d, want 403", rec.Code)
	}
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, testServicePath("web", apiActionRestart), nil)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT without CSRF header = %d, want 403", rec.Code)
	}
	// with the header -> allowed
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, testServicePath("web", apiActionRestart), "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST with CSRF header = %d, want 200", rec.Code)
	}
}

// The Basic challenge belongs on a document load. On a subresource it makes the
// browser throw a modal password box at someone who was only reading the
// dashboard: the page holds an EventSource on /api/stream that the server tells
// to reconnect every 5s, and a reconnect arriving without the cached credential
// used to be answered with WWW-Authenticate. Every request here is still 401.
func TestAuthChallengesDocumentsOnly(t *testing.T) {
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret")})
	tests := []struct {
		name      string
		path      string
		fetchMode string
		accept    string
		challenge bool
	}{
		{name: "root navigation", path: routePathRoot, fetchMode: secFetchModeNavigate, challenge: true},
		{name: "root without headers", path: routePathRoot, challenge: true},
		{name: "login route", path: routePathLogin, fetchMode: "cors", challenge: true},
		{name: "html navigation", path: apiPathServices, fetchMode: secFetchModeNavigate, challenge: true},
		{name: "legacy html client", path: apiPathServices, accept: "text/html,*/*", challenge: true},
		{name: "dashboard poll", path: apiPathServices, fetchMode: "cors", accept: contentTypeJSON},
		{name: "event stream reconnect", path: apiPathServices, fetchMode: "cors", accept: streamContentType},
		{name: "bare api client", path: apiPathServices},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, tc.path, nil)
			if tc.fetchMode != "" {
				r.Header.Set(headerSecFetchMode, tc.fetchMode)
			}
			if tc.accept != "" {
				r.Header.Set(headerAccept, tc.accept)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, r)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			got := rec.Header().Get(headerWWWAuthenticate) != ""
			if got != tc.challenge {
				t.Fatalf("WWW-Authenticate present = %v, want %v", got, tc.challenge)
			}
		})
	}
}

// The realm names the host so operators with many dashboards open can tell
// which password prompt belongs to which machine.
func TestAuthRealmIncludesHostname(t *testing.T) {
	tests := []struct {
		name     string
		hostname string
		want     string
	}{
		{name: "with short host", hostname: "algieba", want: `Basic realm="Sermo algieba"`},
		{name: "empty falls back", hostname: "", want: `Basic realm="Sermo"`},
		{name: "whitespace only falls back", hostname: "  ", want: `Basic realm="Sermo"`},
		{name: "quoted specials escaped", hostname: `a"b\c`, want: `Basic realm="Sermo a\"b\\c"`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := basicAuthChallenge(tc.hostname); got != tc.want {
				t.Fatalf("basicAuthChallenge(%q) = %q, want %q", tc.hostname, got, tc.want)
			}
			h := (&Server{
				Backend:  &fakeBackend{services: []Service{{Name: "web"}}},
				Auth:     Auth{AdminCredentials: testCredentials(t, "secret")},
				Hostname: tc.hostname,
			}).Handler()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, routePathRoot, nil))
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", rec.Code)
			}
			if got := rec.Header().Get(headerWWWAuthenticate); got != tc.want {
				t.Fatalf("WWW-Authenticate = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAdminFullAccess(t *testing.T) {
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret"), GuestCredentials: testCredentials(t, "guestpw")})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, testServicePath("web", apiActionRestart), "admin", "secret"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin action = %d, want 200", rec.Code)
	}
}

// Signalling processes from a browser must clear the same two gates as every other
// mutation: the CSRF header and the admin role. Both are enforced before any
// handler runs, so reap needs no gate of its own.
func TestReapStraysNeedsAdminAndCSRF(t *testing.T) {
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret"), GuestCredentials: testCredentials(t, "guestpw")})
	path := testServicePath("web", apiActionReap)

	rec := httptest.NewRecorder()
	noCSRF := httptest.NewRequest(http.MethodPost, path, nil)
	noCSRF.SetBasicAuth("admin", "secret")
	h.ServeHTTP(rec, noCSRF)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("reap without the CSRF header = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, path, "guest", "guestpw"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest reap = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, path, "admin", "secret"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin reap = %d, want 200", rec.Code)
	}
}

func TestGuestIsReadOnly(t *testing.T) {
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret"), GuestCredentials: testCredentials(t, "guestpw")})
	// guest can read
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, apiPathServices, "guest", "guestpw"))
	if rec.Code != http.StatusOK {
		t.Fatalf("guest read = %d, want 200", rec.Code)
	}
	// guest cannot act
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, testServicePath("web", apiActionRestart), "guest", "guestpw"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest action = %d, want 403", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPut, testServicePath("web", apiActionRestart), "guest", "guestpw"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest unsafe method = %d, want 403", rec.Code)
	}
}

func TestAnonymousGuestReadOnly(t *testing.T) {
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret"), AnonymousGuest: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, apiPathServices, "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous read = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, testServicePath("web", apiActionRestart), "", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous action = %d, want 403", rec.Code)
	}
}

func TestWhoami(t *testing.T) {
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret"), AnonymousGuest: true})
	check := func(user, pass, role string, canAct bool) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req(http.MethodGet, apiPathWhoami, user, pass))
		var got struct {
			Role   string `json:"role"`
			CanAct bool   `json:"can_act"`
			Auth   bool   `json:"auth"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Role != role || got.CanAct != canAct || !got.Auth {
			t.Fatalf("whoami(%s) = %+v, want role=%s canAct=%v", user, got, role, canAct)
		}
	}
	check("admin", "secret", "admin", true)
	check("", "", "guest", false)
}

func TestOpenModeRejectsForeignHosts(t *testing.T) {
	h := (&Server{Backend: &fakeBackend{services: []Service{{Name: "web"}}}, Addr: "127.0.0.1:9797"}).Handler()
	serve := func(host, path string) int {
		r := req(http.MethodGet, path, "", "")
		r.Host = host
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}

	// DNS rebinding lands with the attacker's hostname in Host; the open
	// (auth-less) UI must refuse it.
	if code := serve("evil.example.com", apiPathServices); code != http.StatusMisdirectedRequest {
		t.Fatalf("open mode with foreign Host = %d, want 421", code)
	}
	for _, host := range []string{"localhost:9797", "127.0.0.1:9797", "[::1]:9797", "127.0.0.1"} {
		if code := serve(host, apiPathServices); code != http.StatusOK {
			t.Fatalf("open mode with local Host %q = %d, want 200", host, code)
		}
	}
	// Plain health probes stay reachable for load balancers regardless of Host.
	if code := serve("evil.example.com", routePathLivez); code != http.StatusOK {
		t.Fatalf("plain livez with foreign Host = %d, want 200", code)
	}
}

func TestOpenModeAllowsConfiguredHosts(t *testing.T) {
	h := (&Server{
		Backend:      &fakeBackend{services: []Service{{Name: "web"}}},
		Addr:         "127.0.0.1:9797",
		AllowedHosts: []string{"sermo.internal"},
	}).Handler()
	r := req(http.MethodGet, apiPathServices, "", "")
	r.Host = "sermo.internal:8443"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("allowed_hosts entry = %d, want 200", rec.Code)
	}
}

func TestAuthedModeServesAnyHost(t *testing.T) {
	// With Basic auth on, a rebound origin cannot attach credentials, so the
	// Host check is not applied and reverse proxies keep working.
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret")})
	r := req(http.MethodGet, apiPathServices, "admin", "secret")
	r.Host = "public.example.com"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("authed request with proxy Host = %d, want 200", rec.Code)
	}
}

func TestGuestSeesRedactedCmdlines(t *testing.T) {
	b := &fakeBackend{
		services:      []Service{{Name: "web"}},
		mounts:        []Mount{{Name: "data", Blockers: []MountBlocker{{PID: 9, Cmdline: []string{"rsync", "--password=hunter2", "/data"}}}}},
		mountBlockers: MountBlockersResult{OK: true, Name: "data", Blockers: []MountBlocker{{PID: 9, Cmdline: []string{"rsync", "--password=hunter2", "/data"}}}},
	}
	h := (&Server{Backend: b, Auth: Auth{AdminCredentials: testCredentials(t, "secret"), GuestCredentials: testCredentials(t, "guest")}}).Handler()

	fetch := func(path, pass string, into any) {
		t.Helper()
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req(http.MethodGet, path, "u", pass))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200", path, rec.Code)
		}
		if err := json.Unmarshal(rec.Body.Bytes(), into); err != nil {
			t.Fatal(err)
		}
	}

	var guestDetail Detail
	fetch(testServicePath("web"), "guest", &guestDetail)
	if got := guestDetail.Processes[0].Cmdline; len(got) != 1 || got[0] != "python3" {
		t.Fatalf("guest detail cmdline = %q, want just the executable", got)
	}
	var guestMounts []Mount
	fetch(apiPathMounts, "guest", &guestMounts)
	if got := guestMounts[0].Blockers[0].Cmdline; len(got) != 1 || got[0] != "rsync" {
		t.Fatalf("guest mount blocker cmdline = %q, want just the executable", got)
	}
	var guestBlockers MountBlockersResult
	fetch(testMountPath("data", apiSegmentBlockers), "guest", &guestBlockers)
	if got := guestBlockers.Blockers[0].Cmdline; len(got) != 1 || got[0] != "rsync" {
		t.Fatalf("guest blockers cmdline = %q, want just the executable", got)
	}

	var adminDetail Detail
	fetch(testServicePath("web"), "secret", &adminDetail)
	if got := adminDetail.Processes[0].Cmdline; len(got) != 2 {
		t.Fatalf("admin detail cmdline = %q, want the full command line", got)
	}
	var adminMounts []Mount
	fetch(apiPathMounts, "secret", &adminMounts)
	if got := adminMounts[0].Blockers[0].Cmdline; len(got) != 3 {
		t.Fatalf("admin mount blocker cmdline = %q, want the full command line", got)
	}
	var adminBlockers MountBlockersResult
	fetch(testMountPath("data", apiSegmentBlockers), "secret", &adminBlockers)
	if got := adminBlockers.Blockers[0].Cmdline; len(got) != 3 {
		t.Fatalf("admin blockers cmdline = %q, want the full command line", got)
	}
}

func TestWhoamiWithoutResolvedRoleFailsClosed(t *testing.T) {
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleWhoami(rec, httptest.NewRequest(http.MethodGet, apiPathWhoami, nil))
	var got struct {
		Role   string `json:"role"`
		CanAct bool   `json:"can_act"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Role != roleGuest || got.CanAct {
		t.Fatalf("whoami without role = %+v, want read-only guest (never default to admin)", got)
	}
}

func TestLoginChallengesThenRedirects(t *testing.T) {
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret"), AnonymousGuest: true})
	// a guest hitting /login gets a Basic challenge (to escalate)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, routePathLogin, "", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/login as guest = %d, want 401", rec.Code)
	}
	// with admin creds it redirects home
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, routePathLogin, "admin", "secret"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != routePathRoot {
		t.Fatalf("/login as admin = %d loc=%q, want 303 /", rec.Code, rec.Header().Get("Location"))
	}
}

// Every credential configured for a role grants that role, which is what lets an
// operator rotate a password or give each person their own without a cut.
func TestRoleFromAnyCredential(t *testing.T) {
	hashes := make(map[string]string)
	for _, password := range []string{"first", "second", "hashed-admin", "guest-one", "guest-two"} {
		hash, err := webcred.HashBcrypt(password, webcred.MinBcryptCost)
		if err != nil {
			t.Fatal(err)
		}
		hashes[password] = hash
	}
	adminHash := hashes["hashed-admin"]
	admin, err := webcred.Parse(hashes["first"] + "\n" + hashes["second"] + "\n" + adminHash + "   # ana\n")
	if err != nil {
		t.Fatal(err)
	}
	guest, err := webcred.Parse(hashes["guest-one"] + "\n" + hashes["guest-two"] + "\n")
	if err != nil {
		t.Fatal(err)
	}
	h := authServer(Auth{AdminCredentials: admin, GuestCredentials: guest, RuntimeToken: "run-token"})

	tests := []struct {
		name     string
		password string
		wantRead int
		wantAct  int
	}{
		{name: "first admin credential", password: "first", wantRead: http.StatusOK, wantAct: http.StatusOK},
		{name: "second admin credential", password: "second", wantRead: http.StatusOK, wantAct: http.StatusOK},
		{name: "hashed admin credential", password: "hashed-admin", wantRead: http.StatusOK, wantAct: http.StatusOK},
		{name: "runtime token is admin", password: "run-token", wantRead: http.StatusOK, wantAct: http.StatusOK},
		{name: "first guest credential", password: "guest-one", wantRead: http.StatusOK, wantAct: http.StatusForbidden},
		{name: "second guest credential", password: "guest-two", wantRead: http.StatusOK, wantAct: http.StatusForbidden},
		{name: "the hash itself is not a password", password: adminHash, wantRead: http.StatusUnauthorized, wantAct: http.StatusUnauthorized},
		{name: "the label is not a password", password: "ana", wantRead: http.StatusUnauthorized, wantAct: http.StatusUnauthorized},
		{name: "unknown password", password: "nope", wantRead: http.StatusUnauthorized, wantAct: http.StatusUnauthorized},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req(http.MethodGet, apiPathServices, "anyone", tc.password))
			if rec.Code != tc.wantRead {
				t.Errorf("GET = %d, want %d", rec.Code, tc.wantRead)
			}
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, req(http.MethodPost, testServicePath("web", apiActionRestart), "anyone", tc.password))
			if rec.Code != tc.wantAct {
				t.Errorf("POST = %d, want %d", rec.Code, tc.wantAct)
			}
		})
	}
}

// The runtime token is a way in for sermoctl, never a reason to close an open
// dashboard or to open a closed one beyond admin.
func TestRuntimeTokenDoesNotEnableAuth(t *testing.T) {
	open := Auth{RuntimeToken: "run-token"}
	if open.Enabled() {
		t.Error("Auth with only a runtime token reports auth enabled")
	}
	h := authServer(Auth{GuestCredentials: testCredentials(t, "guestpw"), RuntimeToken: "run-token"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, testServicePath("web", apiActionRestart), "sermoctl", "run-token"))
	if rec.Code != http.StatusOK {
		t.Fatalf("token action with only guest credentials configured = %d, want 200", rec.Code)
	}
}
