package web

import "testing"

func TestParseAPIAccessTarget(t *testing.T) {
	tests := []struct {
		path       string
		wantTarget string
		wantAction string
	}{
		{"/api/services/web/monitor", "web", "monitor"},
		{"/api/watches/storage-root/unmonitor", "storage-root", "unmonitor"},
		{"/api/locks/mysql/release", "mysql", "release"},
		{"/api/reload", "", "reload"},
		// Three-part paths: the target is present even without a trailing action.
		{"/api/services/web", "web", ""},
		{"/api/locks/mysql", "mysql", "release"},
	}
	for _, tc := range tests {
		target, action := parseAPIAccessTarget(tc.path)
		if target != tc.wantTarget || action != tc.wantAction {
			t.Fatalf("parseAPIAccessTarget(%q) = (%q,%q), want (%q,%q)", tc.path, target, action, tc.wantTarget, tc.wantAction)
		}
	}
}
