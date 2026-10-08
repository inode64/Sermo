package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// watchProcessKillBackend adds the optional process-kill capability to the
// shared fake and records every request that reached it.
type watchProcessKillBackend struct {
	*fakeBackend
	watches []string
	killed  []WatchProcessKillRequest
	reject  bool
}

func (b *watchProcessKillBackend) KillWatchProcess(_ context.Context, watch string, req WatchProcessKillRequest) ActionResult {
	b.watches = append(b.watches, watch)
	b.killed = append(b.killed, req)
	if b.reject {
		return ActionResult{OK: false, Message: "the process is no longer listed; refresh and try again"}
	}
	return ActionResult{OK: true, Message: "SIGTERM sent"}
}

func testWatchProcessKillPath(watch, pid string) string {
	return testWatchPath(watch, APISegmentProcesses, pid, APIActionKill)
}

func TestWatchProcessKillEndpointRequiresProcessIdentity(t *testing.T) {
	b := &watchProcessKillBackend{fakeBackend: &fakeBackend{}}
	h := newServer(b)
	validQuery := testQueryParam(APIQueryStartTicks, "987654")
	for _, tc := range []struct{ name, path string }{
		{"missing start_ticks", testWatchProcessKillPath("unowned", "4242")},
		{"zero start_ticks", testPathQuery(testWatchProcessKillPath("unowned", "4242"), testQueryParam(APIQueryStartTicks, "0"))},
		{"text start_ticks", testPathQuery(testWatchProcessKillPath("unowned", "4242"), testQueryParam(APIQueryStartTicks, "soon"))},
		{"negative start_ticks", testPathQuery(testWatchProcessKillPath("unowned", "4242"), testQueryParam(APIQueryStartTicks, "-1"))},
		{"zero pid", testPathQuery(testWatchProcessKillPath("unowned", "0"), validQuery)},
		{"negative pid", testPathQuery(testWatchProcessKillPath("unowned", "-4"), validQuery)},
		{"text pid", testPathQuery(testWatchProcessKillPath("unowned", "init"), validQuery)},
		{"bad escalate", testPathQuery(testWatchProcessKillPath("unowned", "4242"), testQueryParams(APIQueryStartTicks, "987654", APIQueryEscalate, "maybe"))},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postReq(tc.path))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400: %s", tc.name, rec.Code, rec.Body.String())
		}
	}
	if len(b.killed) != 0 {
		t.Fatalf("invalid requests reached the backend: %+v", b.killed)
	}
}

func TestWatchProcessKillEndpointPassesTheDisplayedIdentity(t *testing.T) {
	b := &watchProcessKillBackend{fakeBackend: &fakeBackend{}}
	h := newServer(b)
	path := testWatchProcessKillPath("unowned", "4242")

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(path, testQueryParam(APIQueryStartTicks, "987654"))))
	if rec.Code != http.StatusOK {
		t.Fatalf("kill status = %d: %s", rec.Code, rec.Body.String())
	}
	for _, escalate := range []string{"true", "1"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, postReq(testPathQuery(path, testQueryParams(APIQueryStartTicks, "987654", APIQueryEscalate, escalate))))
		if rec.Code != http.StatusOK {
			t.Fatalf("escalate=%s status = %d: %s", escalate, rec.Code, rec.Body.String())
		}
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(path, testQueryParams(APIQueryStartTicks, "987654", APIQueryEscalate, "false"))))
	if rec.Code != http.StatusOK {
		t.Fatalf("escalate=false status = %d: %s", rec.Code, rec.Body.String())
	}
	want := []WatchProcessKillRequest{
		{PID: 4242, StartTicks: 987654},
		{PID: 4242, StartTicks: 987654, Escalate: true},
		{PID: 4242, StartTicks: 987654, Escalate: true},
		{PID: 4242, StartTicks: 987654},
	}
	if len(b.killed) != len(want) {
		t.Fatalf("killed = %+v, want %+v", b.killed, want)
	}
	for i := range want {
		if b.killed[i] != want[i] || b.watches[i] != "unowned" {
			t.Fatalf("request %d = %+v for %q, want %+v for unowned", i, b.killed[i], b.watches[i], want[i])
		}
	}

	var body ActionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.OK || body.Message != "SIGTERM sent" {
		t.Fatalf("response = %s (%v)", rec.Body.String(), err)
	}

	b.reject = true
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(path, testQueryParam(APIQueryStartTicks, "987654"))))
	if rec.Code != http.StatusConflict {
		t.Fatalf("a rejected kill status = %d, want 409", rec.Code)
	}
}

func TestWatchProcessKillEndpointDecodesTheWatchName(t *testing.T) {
	b := &watchProcessKillBackend{fakeBackend: &fakeBackend{}}
	h := newServer(b)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(testWatchProcessKillPath(url.PathEscape("odd name/here"), "77"), testQueryParam(APIQueryStartTicks, "5"))))
	if rec.Code != http.StatusOK {
		t.Fatalf("kill status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(b.watches) != 1 || b.watches[0] != "odd name/here" || b.killed[0].PID != 77 || b.killed[0].StartTicks != 5 {
		t.Fatalf("watches = %q killed = %+v", b.watches, b.killed)
	}
}

func TestWatchProcessKillEndpointWithoutCapability(t *testing.T) {
	h := newServer(&fakeBackend{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(testWatchProcessKillPath("unowned", "4242"), testQueryParam(APIQueryStartTicks, "987654"))))
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rec.Code)
	}
}

// Signalling a listed process clears the same two gates as every other
// mutation: the CSRF header and the admin role.
func TestWatchProcessKillNeedsAdminAndCSRF(t *testing.T) {
	b := &watchProcessKillBackend{fakeBackend: &fakeBackend{}}
	h := (&Server{Backend: StaticBackend{Backend: b}, Auth: Auth{AdminCredentials: testCredentials(t, "secret"), GuestCredentials: testCredentials(t, "guestpw")}}).Handler()
	path := testPathQuery(testWatchProcessKillPath("unowned", "4242"), testQueryParam(APIQueryStartTicks, "987654"))

	rec := httptest.NewRecorder()
	noCSRF := httptest.NewRequest(http.MethodPost, path, nil)
	noCSRF.SetBasicAuth("admin", "secret")
	h.ServeHTTP(rec, noCSRF)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("kill without the CSRF header = %d, want 403", rec.Code)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, path, "guest", "guestpw"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest kill = %d, want 403", rec.Code)
	}
	if len(b.killed) != 0 {
		t.Fatalf("denied requests reached the backend: %+v", b.killed)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodPost, path, "admin", "secret"))
	if rec.Code != http.StatusOK {
		t.Fatalf("admin kill = %d, want 200", rec.Code)
	}
}

func TestWatchProcessKillAccessLogAction(t *testing.T) {
	target, action := parseAPIAccessTarget(testWatchProcessKillPath("unowned", "4242"))
	if target != "unowned" || action != APIActionKill {
		t.Fatalf("target=%q action=%q", target, action)
	}
}

// The process rows carry no command line, so a guest reads them exactly as an
// admin does; only the hook argv of the same watch is redacted.
func TestGuestReadsWatchProcessesUnredacted(t *testing.T) {
	rows := []WatchProcess{{PID: 4242, StartTicks: 987654, User: "deploy", Exe: "/opt/tools/miner", ExeResolved: true, Reason: "no package owns the executable", CanKill: true}}
	b := &fakeBackend{watches: []Watch{{Name: "unowned", HasHook: true, HookCommand: []string{"/usr/local/bin/push", "--token", "s3cret"}, Processes: rows}}}
	h := (&Server{Backend: StaticBackend{Backend: b}, Auth: Auth{AdminCredentials: testCredentials(t, "secret"), GuestCredentials: testCredentials(t, "guest")}}).Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req(http.MethodGet, APIPathWatches, "u", "guest"))
	if rec.Code != http.StatusOK {
		t.Fatalf("guest watches = %d, want 200", rec.Code)
	}
	var watches []Watch
	if err := json.Unmarshal(rec.Body.Bytes(), &watches); err != nil {
		t.Fatal(err)
	}
	if len(watches) != 1 || len(watches[0].Processes) != 1 || watches[0].Processes[0] != rows[0] {
		t.Fatalf("guest watches = %+v", watches)
	}
	if got := watches[0].HookCommand; len(got) != 1 {
		t.Fatalf("guest hook command = %q, want just the executable", got)
	}
}
