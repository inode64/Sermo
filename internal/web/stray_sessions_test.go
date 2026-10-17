package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type selectedReapBackend struct {
	fakeBackend
	targets []StraySession
}

func (b *selectedReapBackend) ReapProcess(_ context.Context, name string, target StraySession) ActionResult {
	target.Service = name
	b.targets = append(b.targets, target)
	return ActionResult{OK: !b.failOp, Message: "selected reap"}
}

func TestSelectedReapHTTP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		query  string
		fail   bool
		status int
		calls  int
	}{
		{"success", "?start_ticks=100&identity=displayed", false, http.StatusOK, 1},
		{"blocked", "?start_ticks=100&identity=displayed", true, http.StatusConflict, 1},
		{"missing generation", "?identity=displayed", false, http.StatusBadRequest, 0},
		{"missing identity", "?start_ticks=100", false, http.StatusBadRequest, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &selectedReapBackend{failOp: tc.fail}
			rec := httptest.NewRecorder()
			newServer(b).ServeHTTP(rec, postReq("/api/services/web/strays/42/reap"+tc.query))
			if rec.Code != tc.status || len(b.targets) != tc.calls {
				t.Fatalf("status=%d calls=%v body=%s", rec.Code, b.targets, rec.Body.String())
			}
			if tc.calls > 0 && (b.targets[0].PID != 42 || b.targets[0].StartTicks != 100 || b.targets[0].Identity != "displayed") {
				t.Fatalf("target=%+v", b.targets[0])
			}
		})
	}
}

func TestSelectedReapRequiresAdminAndCSRF(t *testing.T) {
	h := authServer(Auth{AdminCredentials: testCredentials(t, "secret"), GuestCredentials: testCredentials(t, "guestpw")})
	path := "/api/services/web/strays/42/reap?start_ticks=100&identity=displayed"
	rec := httptest.NewRecorder()
	noCSRF := httptest.NewRequest(http.MethodPost, path, nil)
	noCSRF.SetBasicAuth("admin", "secret")
	h.ServeHTTP(rec, noCSRF)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, path, "guest", "guestpw"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest: %d", rec.Code)
	}
}
