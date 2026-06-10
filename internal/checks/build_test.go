package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/servicemgr"
)

func TestBuildSkipsDisabledAndUnknown(t *testing.T) {
	section := map[string]any{
		"http":     map[string]any{"type": "http", "url": "http://127.0.0.1/"},
		"off":      map[string]any{"type": "http", "url": "http://127.0.0.1/", "enabled": false},
		"weird":    map[string]any{"type": "nope"},
		"noport":   map[string]any{"type": "tcp"},
		"bin":      map[string]any{"type": "binary", "path": "/usr/sbin/apache2"},
		"shellcmd": map[string]any{"type": "command", "command": []any{"apachectl", "configtest"}},
	}
	built, warnings := Build(section, Deps{Service: "apache", DefaultTimeout: time.Second})

	if len(built) != 3 {
		t.Fatalf("built %d checks, want 3 (http, bin, shellcmd): %+v", len(built), built)
	}
	// disabled is silently skipped; weird type and missing port warn.
	if len(warnings) != 2 {
		t.Fatalf("warnings = %v, want 2", warnings)
	}
}

func TestBuildServiceCheckNeedsStatus(t *testing.T) {
	section := map[string]any{"svc": map[string]any{"type": "service", "expect": "active"}}

	_, warnings := Build(section, Deps{Service: "x", DefaultTimeout: time.Second})
	if len(warnings) != 1 {
		t.Fatalf("expected a warning when Status is nil, got %v", warnings)
	}

	built, warnings := Build(section, Deps{
		Service:        "x",
		DefaultTimeout: time.Second,
		Status:         func(context.Context) (servicemgr.Status, error) { return servicemgr.StatusActive, nil },
	})
	if len(warnings) != 0 || len(built) != 1 {
		t.Fatalf("service check should build with Status: built=%d warnings=%v", len(built), warnings)
	}
}

func TestBuildTimeoutPerCheck(t *testing.T) {
	section := map[string]any{
		"slow":    map[string]any{"type": "binary", "path": "/x", "timeout": "30s"},
		"default": map[string]any{"type": "binary", "path": "/x"},
	}
	built, _ := Build(section, Deps{DefaultTimeout: 7 * time.Second})
	got := map[string]time.Duration{}
	for _, b := range built {
		got[b.Check.Name()] = b.Check.(binaryCheck).timeout
	}
	if got["slow"] != 30*time.Second {
		t.Errorf("slow timeout = %v, want 30s", got["slow"])
	}
	if got["default"] != 7*time.Second {
		t.Errorf("default timeout = %v, want engine default 7s", got["default"])
	}
}

func TestIsHealthType(t *testing.T) {
	tests := []struct {
		typ  string
		want bool
	}{
		{"tcp", true},
		{"ports", true},
		{"autofs", true},
		{"sqlite", true},
		{"websocket", true},
		{"ws", true},
		{"mysql", true},
		{"mariadb", true},
		{"storage", false},
		{"disk", false},
		{"cert", false},
		{"count", false},
		{"sql", false},
		{"mongodb-query", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		if got := IsHealthType(tt.typ); got != tt.want {
			t.Fatalf("IsHealthType(%q) = %v, want %v", tt.typ, got, tt.want)
		}
	}
}

func TestEvaluate(t *testing.T) {
	// Optional failure does not block.
	out := Evaluate([]Result{
		{Check: "a", OK: true},
		{Check: "b", OK: false, Optional: true},
	})
	if !out.OK {
		t.Errorf("optional failure must not make preflight fail")
	}
	// Required failure blocks.
	out = Evaluate([]Result{
		{Check: "a", OK: true},
		{Check: "b", OK: false},
	})
	if out.OK {
		t.Errorf("required failure must make preflight fail")
	}
}
