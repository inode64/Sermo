package web

import (
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
	if user != "" || pass != "" {
		r.SetBasicAuth(user, pass)
	}
	return r
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
