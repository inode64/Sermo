package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"sermo/internal/checks"
)

// dbKillBackend adds the optional query-kill capability to the shared fake.
type dbKillBackend struct {
	*fakeBackend
	killed []DBQuerySession
	modes  []string
	reject bool
}

func (b *dbKillBackend) KillDBQuery(_ context.Context, service string, req DBQueryKillRequest) ActionResult {
	b.killed = append(b.killed, DBQuerySession{Service: service, Watch: req.Watch, ID: req.ID, Identity: req.Identity})
	b.modes = append(b.modes, req.Mode)
	if b.reject {
		return ActionResult{OK: false, Message: "the statement is no longer running; refresh the list"}
	}
	return ActionResult{OK: true, Message: "kill query: cancelled"}
}

func TestDBQueryKillEndpointRequiresStatementIdentity(t *testing.T) {
	b := &dbKillBackend{fakeBackend: &fakeBackend{}}
	h := newServer(b)
	path := testServicePath("mariadb", APISegmentDBQueries, "long-queries", APIActionKill)
	for _, query := range []string{
		"",
		testQueryParams(APIQueryID, "7"),
		testQueryParams(APIQueryID, "0", APIQueryIdentity, "7:70"),
		testQueryParams(APIQueryID, "x", APIQueryIdentity, "7:70"),
		testQueryParams(APIQueryID, "7", APIQueryIdentity, "7:70", APIQueryMode, "nuke"),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postReq(testPathQuery(path, query)))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %q status = %d, want 400", query, rec.Code)
		}
	}
	if len(b.killed) != 0 {
		t.Fatalf("invalid requests reached the backend: %+v", b.killed)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(path, testQueryParams(APIQueryID, "7", APIQueryIdentity, "7:70"))))
	if rec.Code != http.StatusOK {
		t.Fatalf("kill status = %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(path, testQueryParams(APIQueryID, "7", APIQueryIdentity, "7:70", APIQueryMode, checks.DBQueryKillModeConnection))))
	if rec.Code != http.StatusOK {
		t.Fatalf("kill connection status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(b.killed) != 2 || b.killed[0].Service != "mariadb" || b.killed[0].Watch != "long-queries" ||
		b.killed[0].ID != 7 || b.killed[0].Identity != "7:70" || b.modes[0] != checks.DBQueryKillModeQuery || b.modes[1] != checks.DBQueryKillModeConnection {
		t.Fatalf("killed = %+v modes = %v", b.killed, b.modes)
	}

	b.reject = true
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(path, testQueryParams(APIQueryID, "7", APIQueryIdentity, "7:70"))))
	if rec.Code != http.StatusConflict {
		t.Fatalf("a rejected kill status = %d, want 409", rec.Code)
	}
}

func TestDBQueryKillEndpointWithoutCapability(t *testing.T) {
	h := newServer(&fakeBackend{})
	path := testServicePath("mariadb", APISegmentDBQueries, "long-queries", APIActionKill)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(path, testQueryParams(APIQueryID, "7", APIQueryIdentity, "7:70"))))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

func TestDBQueryKillAccessLogAction(t *testing.T) {
	target, action := parseAPIAccessTarget("/api/services/mariadb/db-queries/long-queries/kill")
	if target != "mariadb" || action != APIActionKill {
		t.Fatalf("target=%q action=%q", target, action)
	}
}

func TestSessionInventoryDatabaseJSON(t *testing.T) {
	raw, err := json.Marshal(SessionInventory{Database: []DBQuerySession{{Service: "mariadb", Watch: "w", Engine: "mariadb", ID: 7, Query: "SELECT 1", Identity: "7:70", CanKill: true}}})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	rows, _ := decoded["database"].([]any)
	row, _ := rows[0].(map[string]any)
	if row["can_kill"] != true || row["identity"] != "7:70" || row["elapsed_seconds"] != float64(0) {
		t.Fatalf("database row = %v", row)
	}
}

func TestDBQueryKillPathEscapesItsParts(t *testing.T) {
	if got := DBQueryKillPath("maria db", "long/queries"); got != "/api/services/maria%20db/db-queries/long%2Fqueries/kill" {
		t.Fatalf("DBQueryKillPath = %q", got)
	}
}

func TestNestedServiceRoutesLogTheirLastSegment(t *testing.T) {
	for path, want := range map[string]string{
		"/api/services/web/sessions/96/close":                  apiActionClose,
		"/api/services/web/terminal-sessions/tmux/close-empty": apiActionCloseEmpty,
		"/api/services/web/restart":                            "restart",
		"/api/services/web/button/restart":                     apiSegmentButton,
	} {
		if _, action := parseAPIAccessTarget(path); action != want {
			t.Errorf("%s: action %q, want %q", path, action, want)
		}
	}
}
