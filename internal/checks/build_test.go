package checks

import (
	"context"
	"regexp"
	"strings"
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

func TestCommandExportValueWholeMatchNoGroup(t *testing.T) {
	// A regex without a capture group yields a 1-element submatch, so value()
	// must take match[0] (len > 1, not >= 1, which would index a missing group).
	e := commandExport{regex: regexp.MustCompile("foo+")}
	if got := e.value("xx foooo yy", ""); got != "foooo" {
		t.Fatalf("value = %q, want foooo", got)
	}
}

func TestBuildHTTPCheckBodyFromString(t *testing.T) {
	// A non-empty body string is carried onto the check (s != "").
	c, w := buildHTTPCheck(base{}, map[string]any{"url": "http://127.0.0.1/", "body": "hello"}, nil)
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := string(c.(*httpCheck).body); got != "hello" {
		t.Fatalf("body = %q, want hello", got)
	}
}

func TestParseStatusMatcherClassLowerBoundary(t *testing.T) {
	// "1xx" is the lowest valid status class (s[0] >= '1', not > '1').
	m, err := parseStatusMatcher("1xx")
	if err != nil {
		t.Fatalf("parseStatusMatcher(1xx): %v", err)
	}
	if len(m.classes) != 1 || m.classes[0] != 1 {
		t.Fatalf("classes = %v, want [1]", m.classes)
	}
}

func TestParseStatusMatcherClassUpperBoundary(t *testing.T) {
	// "5xx" is the highest valid status class (s[0] <= '5', not < '5').
	m, err := parseStatusMatcher("5xx")
	if err != nil {
		t.Fatalf("parseStatusMatcher(5xx): %v", err)
	}
	if len(m.classes) != 1 || m.classes[0] != 5 {
		t.Fatalf("classes = %v, want [5]", m.classes)
	}
	// "6xx" is out of range and must be rejected.
	if _, err := parseStatusMatcher("6xx"); err == nil {
		t.Fatal("6xx must be rejected")
	}
}

func TestBuildCertCheckServerNameDefaultsToHost(t *testing.T) {
	c, w := buildCertCheck(base{}, map[string]any{"host": "example.com"}, Deps{})
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := c.(*certCheck).serverName; got != "example.com" {
		t.Fatalf("serverName = %q, want example.com (defaults to host)", got)
	}
	c2, _ := buildCertCheck(base{}, map[string]any{"host": "example.com", "server_name": "sni.example.com"}, Deps{})
	if got := c2.(*certCheck).serverName; got != "sni.example.com" {
		t.Fatalf("explicit serverName = %q, want sni.example.com", got)
	}
}

func TestBuildMetricCheckNameRequired(t *testing.T) {
	// Missing name is rejected with a name-specific warning...
	if _, w := buildMetricCheck(base{}, map[string]any{"op": ">"}, Deps{}); !strings.Contains(w, "requires a name") {
		t.Fatalf("missing name warning = %q, want it to mention requiring a name", w)
	}
	// ...and a present name does not trigger that warning.
	if _, w := buildMetricCheck(base{}, map[string]any{"name": "cpu", "op": ">"}, Deps{}); strings.Contains(w, "requires a name") {
		t.Fatalf("present name must not warn about a missing name, got %q", w)
	}
}

func TestBuildMetricCheckOpRequired(t *testing.T) {
	if _, w := buildMetricCheck(base{}, map[string]any{"name": "cpu"}, Deps{}); !strings.Contains(w, "requires an op") {
		t.Fatalf("missing op warning = %q, want it to mention requiring an op", w)
	}
	if _, w := buildMetricCheck(base{}, map[string]any{"name": "cpu", "op": ">"}, Deps{}); strings.Contains(w, "requires an op") {
		t.Fatalf("present op must not warn about a missing op, got %q", w)
	}
}

func TestBuildHdparmCheckDeviceRequired(t *testing.T) {
	if _, w := buildHdparmCheck(base{}, map[string]any{}, nil); !strings.Contains(w, "requires a device") {
		t.Fatalf("missing device warning = %q, want it to mention requiring a device", w)
	}
	if _, w := buildHdparmCheck(base{}, map[string]any{"device": "/dev/sda", "read": map[string]any{"op": "<", "value": 1}}, nil); strings.Contains(w, "requires a device") {
		t.Fatalf("present device must not warn about a missing device, got %q", w)
	}
}

func TestBuildSmartCheckDeviceRequired(t *testing.T) {
	if _, w := buildSmartCheck(base{}, map[string]any{}, nil); !strings.Contains(w, "requires a device") {
		t.Fatalf("missing device warning = %q, want it to mention requiring a device", w)
	}
	if _, w := buildSmartCheck(base{}, map[string]any{"device": "/dev/sda"}, nil); strings.Contains(w, "requires a device") {
		t.Fatalf("present device must not warn about a missing device, got %q", w)
	}
}

func TestBuildICMPCheckHostRequired(t *testing.T) {
	if _, w := buildICMPCheck(base{}, map[string]any{"metric": "state", "expect": "up"}, Deps{}); !strings.Contains(w, "requires a host") {
		t.Fatalf("missing host warning = %q, want it to mention requiring a host", w)
	}
	if _, w := buildICMPCheck(base{}, map[string]any{"host": "127.0.0.1", "metric": "state", "expect": "up"}, Deps{}); strings.Contains(w, "requires a host") {
		t.Fatalf("present host must not warn about a missing host, got %q", w)
	}
}

func TestBuildICMPCheckCountPositive(t *testing.T) {
	// count == 0 is rejected (v <= 0, not v < 0)...
	if _, w := buildICMPCheck(base{}, map[string]any{"host": "127.0.0.1", "count": 0, "metric": "state", "expect": "up"}, Deps{}); !strings.Contains(w, "positive integer") {
		t.Fatalf("count 0 warning = %q, want it to demand a positive integer", w)
	}
	// ...a positive count is accepted.
	if _, w := buildICMPCheck(base{}, map[string]any{"host": "127.0.0.1", "count": 3, "metric": "state", "expect": "up"}, Deps{}); strings.Contains(w, "positive integer") {
		t.Fatalf("positive count must not warn, got %q", w)
	}
}

func TestBuildICMPStateRequiresExpectOrOnChange(t *testing.T) {
	// metric=state with neither expect nor on:change is rejected.
	if _, w := buildICMPCheck(base{}, map[string]any{"host": "127.0.0.1", "metric": "state"}, Deps{}); !strings.Contains(w, "requires expect") {
		t.Fatalf("icmp state w/o expect warning = %q, want it to require expect/on", w)
	}
	// With an expect it is accepted.
	if _, w := buildICMPCheck(base{}, map[string]any{"host": "127.0.0.1", "metric": "state", "expect": "up"}, Deps{}); strings.Contains(w, "requires expect") {
		t.Fatalf("icmp state with expect must not warn, got %q", w)
	}
}

func TestBuildNetStateRequiresExpectOrOnChange(t *testing.T) {
	if _, w := buildNetCheck(base{}, map[string]any{"interface": "eth0", "metric": "state"}, Deps{}); !strings.Contains(w, "requires expect") {
		t.Fatalf("net state w/o expect warning = %q, want it to require expect/on", w)
	}
	if _, w := buildNetCheck(base{}, map[string]any{"interface": "eth0", "metric": "state", "expect": "up"}, Deps{}); strings.Contains(w, "requires expect") {
		t.Fatalf("net state with expect must not warn, got %q", w)
	}
}

func TestBuildNetStateExpectValidated(t *testing.T) {
	if _, w := buildNetCheck(base{}, map[string]any{"interface": "eth0", "metric": "state", "expect": "sideways"}, Deps{}); !strings.Contains(w, "up or down") {
		t.Fatalf("invalid expect warning = %q, want it to require up or down", w)
	}
	if _, w := buildNetCheck(base{}, map[string]any{"interface": "eth0", "metric": "state", "expect": "up"}, Deps{}); strings.Contains(w, "up or down") {
		t.Fatalf("valid expect must not warn, got %q", w)
	}
}

func TestBuildAutofsCheckCountValueNumeric(t *testing.T) {
	// A numeric count value builds cleanly...
	if _, w := buildAutofsCheck(base{}, map[string]any{"count": map[string]any{"op": ">", "value": "5"}}, Deps{}); w != "" {
		t.Fatalf("numeric count value warned: %q", w)
	}
	// ...a non-numeric one is rejected.
	if _, w := buildAutofsCheck(base{}, map[string]any{"count": map[string]any{"op": ">", "value": "abc"}}, Deps{}); !strings.Contains(w, "must be numeric") {
		t.Fatalf("non-numeric count warning = %q, want it to require numeric", w)
	}
}

func TestBuildConfigCheckRequiresCommandOrPath(t *testing.T) {
	// Neither command nor path -> rejected.
	if _, w := buildConfigCheck(base{}, map[string]any{}, nil); !strings.Contains(w, "requires a command and/or path") {
		t.Fatalf("empty config warning = %q, want command/path requirement", w)
	}
	// A command alone is enough...
	if _, w := buildConfigCheck(base{}, map[string]any{"command": []any{"nginx", "-t"}}, nil); w != "" {
		t.Fatalf("command-only config warned: %q", w)
	}
	// ...and a path alone is enough.
	if _, w := buildConfigCheck(base{}, map[string]any{"path": "/etc/nginx/nginx.conf"}, nil); w != "" {
		t.Fatalf("path-only config warned: %q", w)
	}
}

func TestBuildSensorsCheckRequiresPredicate(t *testing.T) {
	// No predicate -> the requireLevelPreds warning is surfaced, not swallowed.
	if _, w := buildSensorsCheck(base{}, map[string]any{"chip": "coretemp"}, Deps{}); !strings.Contains(w, "sensors check") {
		t.Fatalf("predicate-less sensors warning = %q, want a sensors check warning", w)
	}
}
