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
		r.Header.Set(csrfHeader, "1")
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/livez = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok\n" {
		t.Fatalf("/livez body = %q, want \"ok\\n\"", got)
	}
	// a normal endpoint still challenges
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/services", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/api/services without auth = %d, want 401", rec.Code)
	}
}

func TestReadyzPublicEvenWithAuth(t *testing.T) {
	h := (&Server{
		Backend:   &fakeBackend{services: []Service{{Name: "web"}}},
		Auth:      Auth{AdminPassword: "secret"},
		Readiness: fakeReadiness{rep: ReadyReport{Ready: true, Status: "ok", Services: 1}},
	}).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/readyz = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "ok\n" {
		t.Fatalf("/readyz body = %q", got)
	}
}

func TestVerboseHealthRequiresAuth(t *testing.T) {
	h := (&Server{
		Backend:   &fakeBackend{services: []Service{{Name: "web"}}},
		Auth:      Auth{AdminPassword: "secret"},
		Readiness: fakeReadiness{rep: ReadyReport{Ready: true, Status: "ok", Services: 1}},
	}).Handler()
	for _, path := range []string{"/livez?verbose", "/readyz?verbose"} {
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/readyz?verbose", nil))
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
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/livez?verbose", nil))
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
	if got.Status != "ok" || got.Uptime == "" || got.Services != 1 || got.Go == "" {
		t.Fatalf("unexpected livez verbose: %+v", got)
	}
}

func TestCSRFGuardOnUnsafeMethods(t *testing.T) {
	h := authServer(Auth{}) // open mode: even without auth, a forged request is blocked
	// no CSRF header -> rejected
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/services/web/restart", nil)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF header = %d, want 403", rec.Code)
	}
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodPut, "/api/services/web/restart", nil)
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT without CSRF header = %d, want 403", rec.Code)
	}
	// with the header -> allowed
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, "/api/services/web/restart", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST with CSRF header = %d, want 200", rec.Code)
	}
}

func TestAuthDisabledIsOpen(t *testing.T) {
	h := authServer(Auth{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, "/api/services/web/restart", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("open server should allow actions, got %d", rec.Code)
	}
}

func TestAuthRequiredChallenges(t *testing.T) {
	h := authServer(Auth{AdminPassword: "secret"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, "/api/services", "", ""))
	if rec.Code != http.StatusUnauthorized || rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatalf("expected 401 challenge, got %d (%q)", rec.Code, rec.Header().Get("WWW-Authenticate"))
	}
}

func TestAdminFullAccess(t *testing.T) {
	h := authServer(Auth{AdminPassword: "secret", GuestPassword: "guestpw"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, "/api/services/web/restart", "admin", "secret"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin action = %d, want 200", rec.Code)
	}
}

func TestGuestIsReadOnly(t *testing.T) {
	h := authServer(Auth{AdminPassword: "secret", GuestPassword: "guestpw"})
	// guest can read
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, "/api/services", "guest", "guestpw"))
	if rec.Code != http.StatusOK {
		t.Fatalf("guest read = %d, want 200", rec.Code)
	}
	// guest cannot act
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, "/api/services/web/restart", "guest", "guestpw"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest action = %d, want 403", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPut, "/api/services/web/restart", "guest", "guestpw"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest unsafe method = %d, want 403", rec.Code)
	}
}

func TestAnonymousGuestReadOnly(t *testing.T) {
	h := authServer(Auth{AdminPassword: "secret", AnonymousGuest: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, "/api/services", "", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous read = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, "/api/services/web/restart", "", ""))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous action = %d, want 403", rec.Code)
	}
}

func TestWhoami(t *testing.T) {
	h := authServer(Auth{AdminPassword: "secret", AnonymousGuest: true})
	check := func(user, pass, role string, canAct bool) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req(http.MethodGet, "/api/whoami", user, pass))
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

func TestLoginChallengesThenRedirects(t *testing.T) {
	h := authServer(Auth{AdminPassword: "secret", AnonymousGuest: true})
	// a guest hitting /login gets a Basic challenge (to escalate)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, "/login", "", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/login as guest = %d, want 401", rec.Code)
	}
	// with admin creds it redirects home
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, "/login", "admin", "secret"))
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Fatalf("/login as admin = %d loc=%q, want 303 /", rec.Code, rec.Header().Get("Location"))
	}
}
