package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"sermo/internal/logfile"
	"sermo/internal/mountctl"
)

func TestParseAPIAccessTarget(t *testing.T) {
	tests := []struct {
		path       string
		wantTarget string
		wantAction string
	}{
		{testServicePath("web", apiActionMonitor), "web", apiActionMonitor},
		{testWatchPath("storage-root", apiActionUnmonitor), "storage-root", apiActionUnmonitor},
		{testMountPath("backup", mountctl.ActionUmount), "backup", mountctl.ActionUmount},
		{testMountPath("backup", mountctl.ActionMount), "backup", mountctl.ActionMount},
		{testTargetPath(apiSegmentNotifiers, "ops", apiActionTest), "ops", apiActionTest},
		{testLockPath("mysql", apiActionRelease), "mysql", apiActionRelease},
		{apiPathReload, "", apiActionReload},
		// Three-part paths: the target is present even without a trailing action.
		{testServicePath("web"), "web", ""},
		{testMountPath("backup"), "backup", ""},
		{testLockPath("mysql"), "mysql", apiActionRelease},
	}
	for _, tc := range tests {
		target, action := parseAPIAccessTarget(tc.path)
		if target != tc.wantTarget || action != tc.wantAction {
			t.Fatalf("parseAPIAccessTarget(%q) = (%q,%q), want (%q,%q)", tc.path, target, action, tc.wantTarget, tc.wantAction)
		}
	}
}

func TestRecordWebAccessLogsMountActionAndQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	log, err := logfile.Open(path)
	if err != nil {
		t.Fatalf("logfile.Open: %v", err)
	}
	defer log.Close()
	s := &Server{AccessLog: log}

	r := httptest.NewRequest(
		http.MethodPost,
		testPathQuery(testMountPath("backup", mountctl.ActionUmount), testQueryParam(apiQueryKill, queryBoolOne)),
		nil,
	)
	s.recordWebAccess(r, http.StatusOK)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("access log line = %q: %v", data, err)
	}
	if entry[accessFieldTarget] != "backup" || entry[accessFieldAction] != mountctl.ActionUmount {
		t.Fatalf("access entry = %v, want target=backup action=umount", entry)
	}
	if entry[accessFieldQuery] != "kill=1" {
		t.Fatalf("access entry query = %v, want kill=1", entry[accessFieldQuery])
	}

	// A queryless action must not write an empty query field.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s.recordWebAccess(httptest.NewRequest(http.MethodPost, testServicePath("web", apiActionRestart), nil), http.StatusOK)
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	entry = map[string]any{}
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("access log line = %q: %v", data, err)
	}
	if _, ok := entry[accessFieldQuery]; ok {
		t.Fatalf("access entry = %v, want no query field", entry)
	}
}

func TestAccessLogRecordsAuthDeniedPost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	log, err := logfile.Open(path)
	if err != nil {
		t.Fatalf("logfile.Open: %v", err)
	}
	defer log.Close()

	s := &Server{
		Backend:   &fakeBackend{services: []Service{{Name: "web"}}},
		Auth:      Auth{AdminPassword: "secret", GuestPassword: "guestpw"},
		AccessLog: log,
	}
	h := s.Handler()

	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, testServicePath("web", apiActionRestart), nil)
	r.Header.Set(headerSermoCSRF, "1")
	r.SetBasicAuth("guest", "guestpw")
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("guest action = %d, want 403", rec.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("access log line = %q: %v", data, err)
	}
	if entry[accessFieldActor] != roleGuest || entry[accessFieldStatus] != float64(http.StatusForbidden) {
		t.Fatalf("access entry = %v, want actor=guest status=403", entry)
	}
	if entry[accessFieldTarget] != "web" || entry[accessFieldAction] != apiActionRestart {
		t.Fatalf("access entry = %v, want target=web action=restart", entry)
	}
}

func TestAccessLogRecordsUnsafeDeniedMethods(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.log")
	log, err := logfile.Open(path)
	if err != nil {
		t.Fatalf("logfile.Open: %v", err)
	}
	defer log.Close()

	s := &Server{
		Backend:   &fakeBackend{services: []Service{{Name: "web"}}},
		Auth:      Auth{AdminPassword: "secret"},
		AccessLog: log,
	}
	h := s.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, testServicePath("web", apiActionRestart), nil))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("unsafe method without CSRF = %d, want 403", rec.Code)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("access log line = %q: %v", data, err)
	}
	if entry[accessFieldMethod] != http.MethodPut || entry[accessFieldActor] != accessActorAnonymous {
		t.Fatalf("access entry = %v, want anonymous PUT", entry)
	}
	if entry[accessFieldStatus] != float64(http.StatusForbidden) ||
		entry[accessFieldTarget] != "web" || entry[accessFieldAction] != apiActionRestart {
		t.Fatalf("access entry = %v, want denied web restart", entry)
	}
}
