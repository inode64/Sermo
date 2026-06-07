package web

import (
	"context"
	"crypto/subtle"
	"net/http"
)

// Auth controls access to the dashboard via HTTP Basic auth with two roles:
//
//   - admin: full access (read and actions). Granted by AdminPassword.
//   - guest: read-only (GET only; POST actions are refused). Granted by
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

// role resolves a request to "admin", "guest", or "" (unauthenticated).
func (a Auth) role(r *http.Request) string {
	if !a.Enabled() {
		return "admin"
	}
	if _, pass, ok := r.BasicAuth(); ok {
		if a.AdminPassword != "" && secureEqual(pass, a.AdminPassword) {
			return "admin"
		}
		if a.GuestPassword != "" && secureEqual(pass, a.GuestPassword) {
			return "guest"
		}
	}
	if a.AnonymousGuest {
		return "guest"
	}
	return ""
}

type roleCtxKey struct{}

func roleFrom(ctx context.Context) string {
	role, _ := ctx.Value(roleCtxKey{}).(string)
	return role
}

// withAuth enforces the role on each request: unauthenticated requests get a Basic
// challenge, guests may only read (GET), and /login is an admin-only endpoint that
// triggers the browser's login prompt then redirects home (used to escalate from
// anonymous guest to admin).
func (s *Server) withAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Liveness is a public probe: monitors and load balancers carry no
		// credentials, so /livez bypasses authentication entirely.
		if r.URL.Path == "/livez" || r.URL.Path == "/readyz" {
			next.ServeHTTP(w, r)
			return
		}

		role := s.Auth.role(r)

		if r.URL.Path == "/login" {
			if role == "admin" {
				http.Redirect(w, r, "/", http.StatusSeeOther)
			} else {
				s.challenge(w)
			}
			return
		}
		// CSRF: state-changing requests must carry the custom header (set by the
		// dashboard's fetch). Checked before auth so a forged cross-site POST is
		// rejected even when the browser would attach cached credentials.
		if r.Method == http.MethodPost && r.Header.Get(csrfHeader) == "" {
			writeJSON(w, http.StatusForbidden, ActionResult{OK: false, Message: "missing " + csrfHeader + " header (CSRF protection)"})
			return
		}
		if role == "" {
			s.challenge(w)
			return
		}
		if r.Method == http.MethodPost && role != "admin" {
			writeJSON(w, http.StatusForbidden, ActionResult{OK: false, Message: "read-only access"})
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), roleCtxKey{}, role)))
	})
}

func (s *Server) challenge(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Basic realm="Sermo"`)
	writeJSON(w, http.StatusUnauthorized, ActionResult{OK: false, Message: "authentication required"})
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	role := roleFrom(r.Context())
	if role == "" {
		role = "admin"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"role":    role,
		"can_act": role == "admin",
		"auth":    s.Auth.Enabled(),
	})
}

func secureEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
