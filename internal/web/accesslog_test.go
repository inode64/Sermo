package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"sermo/internal/logfile"
)

func TestParseAPIAccessTarget(t *testing.T) {
	tests := []struct {
		path       string
		wantTarget string
		wantAction string
	}{
		{"/api/services/web/monitor", "web", "monitor"},
		{"/api/watches/storage-root/unmonitor", "storage-root", "unmonitor"},
		{"/api/mounts/backup/umount", "backup", "umount"},
		{"/api/mounts/backup/mount", "backup", "mount"},
		{"/api/locks/mysql/release", "mysql", "release"},
		{"/api/reload", "", "reload"},
		// Three-part paths: the target is present even without a trailing action.
		{"/api/services/web", "web", ""},
		{"/api/mounts/backup", "backup", ""},
		{"/api/locks/mysql", "mysql", "release"},
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

	r := httptest.NewRequest("POST", "/api/mounts/backup/umount?kill=1", nil)
	s.recordWebAccess(r, 200)

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read access log: %v", err)
	}
	var entry map[string]any
	if err := json.Unmarshal(data, &entry); err != nil {
		t.Fatalf("access log line = %q: %v", data, err)
	}
	if entry[accessFieldTarget] != "backup" || entry[accessFieldAction] != "umount" {
		t.Fatalf("access entry = %v, want target=backup action=umount", entry)
	}
	if entry[accessFieldQuery] != "kill=1" {
		t.Fatalf("access entry query = %v, want kill=1", entry[accessFieldQuery])
	}

	// A queryless action must not write an empty query field.
	if err := os.Truncate(path, 0); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	s.recordWebAccess(httptest.NewRequest("POST", "/api/services/web/restart", nil), 200)
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
