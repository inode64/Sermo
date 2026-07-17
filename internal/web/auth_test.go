package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
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

type fakeReadiness struct{ rep ReadyReport }

func (f fakeReadiness) Report(_ context.Context) ReadyReport { return f.rep }

func TestLivezPublicEvenWithAuth(t *testing.T) {
	// auth required for everything else, but /livez must answer without credentials
	h := authServer(Auth{AdminPassword: "secret"})
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
		Auth:      Auth{AdminPassword: "secret"},
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
		Auth:      Auth{AdminPassword: "secret"},
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

func TestAuthDisabledIsOpen(t *testing.T) {
	h := authServer(Auth{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, testServicePath("web", apiActionRestart), "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("open server should allow actions, got %d", rec.Code)
	}
}

func TestAuthRequiredChallenges(t *testing.T) {
	h := authServer(Auth{AdminPassword: "secret"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, apiPathServices, "", ""))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("expected 401 challenge, got %d (%q)", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestAdminFullAccess(t *testing.T) {
	h := authServer(Auth{AdminPassword: "secret", GuestPassword: "guestpw"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, testServicePath("web", apiActionRestart), "admin", "secret"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin action = %d, want 200", rec.Code)
	}
}

func TestGuestIsReadOnly(t *testing.T) {
	h := authServer(Auth{AdminPassword: "secret", GuestPassword: "guestpw"})
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
	h := authServer(Auth{AdminPassword: "secret", AnonymousGuest: true})
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
	h := authServer(Auth{AdminPassword: "secret", AnonymousGuest: true})
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
	h := authServer(Auth{AdminPassword: "secret"})
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
		services: []Service{{Name: "web"}},
		mounts:   []Mount{{Name: "data", Blockers: []MountBlocker{{PID: 9, Cmdline: []string{"rsync", "--password=hunter2", "/data"}}}}},
	}
	h := (&Server{Backend: b, Auth: Auth{AdminPassword: "secret", GuestPassword: "guest"}}).Handler()

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
	h := authServer(Auth{AdminPassword: "secret", AnonymousGuest: true})
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
