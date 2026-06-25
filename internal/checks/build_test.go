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

func TestBuildWithWarningsReturnsOutcomeResults(t *testing.T) {
	section := map[string]any{
		"metric":  map[string]any{"type": "metric", "name": "cpu", "op": ">", "value": "90"},
		"process": map[string]any{"type": "process", "exe": "/usr/bin/app", "optional": true},
	}

	built, warnings := BuildWithWarnings(section, Deps{Service: "web", DefaultTimeout: time.Second})
	if len(built) != 0 || len(warnings) != 2 {
		t.Fatalf("built=%d warnings=%v, want no built checks and two warnings", len(built), warnings)
	}

	results := BuildWarningResults(warnings)
	out := Evaluate(results)
	if out.OK {
		t.Fatalf("required build warning must make outcome fail: %+v", out)
	}
	if results[0].Service != "web" || results[0].Check != "metric" || results[0].Optional {
		t.Fatalf("required warning result = %+v, want service/check and non-optional", results[0])
	}
	if results[1].Check != "process" || !results[1].Optional {
		t.Fatalf("optional warning result = %+v, want optional process warning", results[1])
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
		{"ws", false},
		{"firewall_rules", true},
		{"process", true},
		{"lockfile", true},
		{"mysql", true},
		{"mariadb", true},
		{"storage", false},
		{"cert", true}, // health-style: OK means the certificate is acceptable
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

func TestBuildMarksConditionChecks(t *testing.T) {
	built, warnings := Build(map[string]any{
		"load": map[string]any{"type": "load", "load1": map[string]any{"op": ">", "value": 1}},
		"tcp":  map[string]any{"type": "tcp", "host": "127.0.0.1", "port": 1},
	}, Deps{})
	if len(warnings) != 0 || len(built) != 2 {
		t.Fatalf("built=%d warnings=%v, want two checks", len(built), warnings)
	}
	got := map[string]bool{}
	for _, b := range built {
		got[b.Check.Name()] = b.Check.Run(context.Background()).Condition
	}
	if !got["load"] || got["tcp"] {
		t.Fatalf("condition flags = %+v, want load=true tcp=false", got)
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

func TestBuildTCPCheckHostDefault(t *testing.T) {
	// No host -> default loopback; an explicit host is preserved (the default
	// only applies when host == "").
	c, w := buildTCPCheck(base{}, map[string]any{"port": 80})
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := c.(tcpCheck).host; got != "127.0.0.1" {
		t.Fatalf("default host = %q, want 127.0.0.1", got)
	}
	c2, _ := buildTCPCheck(base{}, map[string]any{"port": 80, "host": "example.test"})
	if got := c2.(tcpCheck).host; got != "example.test" {
		t.Fatalf("explicit host = %q, want example.test", got)
	}
}

func TestBuildPortsCheckHostDefault(t *testing.T) {
	c, w := buildPortsCheck(base{}, map[string]any{"ports": "80"})
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := c.(*portsCheck).host; got != "127.0.0.1" {
		t.Fatalf("default host = %q, want 127.0.0.1", got)
	}
	c2, _ := buildPortsCheck(base{}, map[string]any{"ports": "80", "host": "ports.test"})
	if got := c2.(*portsCheck).host; got != "ports.test" {
		t.Fatalf("explicit host = %q, want ports.test", got)
	}
}

func TestBuildPortsCheckMatchDefault(t *testing.T) {
	// Absent match defaults to "all".
	c, w := buildPortsCheck(base{}, map[string]any{"ports": "80"})
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := c.(*portsCheck).match; got != "all" {
		t.Fatalf("default match = %q, want all", got)
	}
}

func TestBuildPortsCheckMatchValidation(t *testing.T) {
	// Every supported match value is accepted (no warning)...
	for _, m := range []string{"all", "any", "none"} {
		if _, w := buildPortsCheck(base{}, map[string]any{"ports": "80", "match": m}); w != "" {
			t.Fatalf("match %q rejected: %q", m, w)
		}
	}
	// ...and an unsupported one is rejected.
	if _, w := buildPortsCheck(base{}, map[string]any{"ports": "80", "match": "most"}); w == "" {
		t.Fatal("match \"most\" must be rejected")
	}
}

func TestBuildFileExistsCheckPathRequired(t *testing.T) {
	// A path is required; when present it is carried onto the check.
	c, w := buildFileExistsCheck(base{}, map[string]any{"path": "/etc/hostname"})
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := c.(fileExistsCheck).path; got != "/etc/hostname" {
		t.Fatalf("path = %q, want /etc/hostname", got)
	}
	if _, w := buildFileExistsCheck(base{}, map[string]any{}); w == "" {
		t.Fatal("missing path must warn")
	}
}

func TestBuildLibrariesCheckBinaryRequired(t *testing.T) {
	c, w := buildLibrariesCheck(base{}, map[string]any{"binary": "/usr/bin/ssh"})
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := c.(librariesCheck).binary; got != "/usr/bin/ssh" {
		t.Fatalf("binary = %q, want /usr/bin/ssh", got)
	}
	if _, w := buildLibrariesCheck(base{}, map[string]any{}); w == "" {
		t.Fatal("missing binary must warn")
	}
}

func TestBuildProcessCheckStateDefault(t *testing.T) {
	deps := Deps{Processes: func(exe, user string) string { return "running" }}
	c, w := buildProcessCheck(base{}, map[string]any{"exe": "sshd"}, deps)
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := c.(processCheck).expect; got != "running" {
		t.Fatalf("default state = %q, want running", got)
	}
	c2, _ := buildProcessCheck(base{}, map[string]any{"exe": "sshd", "state": "zombie"}, deps)
	if got := c2.(processCheck).expect; got != "zombie" {
		t.Fatalf("explicit state = %q, want zombie", got)
	}
}
