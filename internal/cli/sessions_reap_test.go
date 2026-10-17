package cli

import (
	"net/http"
	"strings"
	"testing"

	"sermo/internal/web"
)

func TestSessionsReapSendsOnlyListedIdentity(t *testing.T) {
	for _, authorized := range []bool{false, true} {
		t.Run(map[bool]string{false: "blocked", true: "authorized"}[authorized], func(t *testing.T) {
			posted := make(chan string, 1)
			daemon := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Method {
				case http.MethodHead:
					w.Header().Set(web.HeaderGeneration, "7")
				case http.MethodGet:
					writeDaemonAPITestJSON(w, web.SessionInventory{Strays: []web.StraySession{{
						Service: "sshd", PID: 42, StartTicks: 100, Identity: "exact", CanClose: authorized, CloseReason: "missing reap policy",
					}}})
				case http.MethodPost:
					posted <- r.URL.String()
					if r.Header.Get(web.HeaderGeneration) != "7" || r.Header.Get(web.HeaderCSRF) == "" {
						http.Error(w, "missing safety headers", http.StatusPreconditionRequired)
						return
					}
					writeDaemonAPITestJSON(w, web.ActionResult{OK: true, Message: "selected process exited"})
				}
			})
			app, stdout, stderr, global := sessionsTestApp(t, daemon)
			code := app.Run(t.Context(), []string{"--config", global, "sessions", "reap", "sshd", "42"})
			if authorized {
				if code != exitSuccess || !strings.Contains(stdout.String(), "selected process exited") {
					t.Fatalf("code=%d out=%s err=%s", code, stdout, stderr)
				}
				select {
				case path := <-posted:
					if path != "/api/services/sshd/strays/42/reap?identity=exact&start_ticks=100" {
						t.Fatalf("unexpected selection %s", path)
					}
				default:
					t.Fatal("no selected reap request")
				}
			} else if code == exitSuccess || len(posted) != 0 {
				t.Fatalf("unauthorized request code=%d posted=%d", code, len(posted))
			}
		})
	}
}
