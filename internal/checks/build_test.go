package checks

import (
	"context"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"sermo/internal/metrics"
	"sermo/internal/servicemgr"
)

func TestBuildDependencies(t *testing.T) {
	runner, client := buildDependencies(Deps{})
	if runner == nil || client == nil {
		t.Fatalf("default dependencies = %T, %v; want runner and client", runner, client)
	}

	customClient := &http.Client{}
	runner, client = buildDependencies(Deps{Runner: fakeRunner{}, HTTPClient: customClient})
	if _, ok := runner.(fakeRunner); !ok || client != customClient {
		t.Fatalf("custom dependencies = %T, %v; want fake runner and supplied client", runner, client)
	}
}

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
	if _, warnings := Build(map[string]any{
		"bad": map[string]any{"type": "binary", "path": "/x", "timeout": "slow"},
	}, Deps{DefaultTimeout: 7 * time.Second}); len(warnings) == 0 {
		t.Fatal("invalid timeout should warn")
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

// buildOneCheck injects typ into entry, builds the single named section with
// deps, and returns the built check; the caller casts to the concrete type.
func buildOneCheck(t *testing.T, name, typ string, entry map[string]any, deps Deps) Check {
	t.Helper()
	entry["type"] = typ
	built, warns := Build(map[string]any{name: entry}, deps)
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("%s check should build: warns=%v", typ, warns)
	}
	return built[0].Check
}

// assertBuildThresholdFires builds a typ check with the given predicate entry and
// deps, asserts it builds cleanly and fires, then asserts a predicate-less entry
// warns.
func assertBuildThresholdFires(t *testing.T, typ string, entry map[string]any, deps Deps) {
	t.Helper()
	if !buildOneCheck(t, "c", typ, entry, deps).Run(context.Background()).OK {
		t.Fatalf("%s should build and fire", typ)
	}
	if _, warns := Build(map[string]any{"c": map[string]any{"type": typ}}, Deps{}); len(warns) == 0 {
		t.Fatalf("%s check without a predicate should warn", typ)
	}
}

// assertRequiredStringField asserts build carries the present entry's field
// onto the check without warning, and warns when the entry is empty.
func assertRequiredStringField(t *testing.T, build func(map[string]any) (Check, string), present map[string]any, field func(Check) string, want string) {
	t.Helper()
	c, w := build(present)
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := field(c); got != want {
		t.Fatalf("field = %q, want %q", got, want)
	}
	if _, w := build(map[string]any{}); w == "" {
		t.Fatal("missing field must warn")
	}
}

// assertRequiredField asserts build warns (containing substr) for the absent
// entry and does not emit that warning for the present entry.
func assertRequiredField(t *testing.T, build func(map[string]any) (Check, string), absent, present map[string]any, substr string) {
	t.Helper()
	if _, w := build(absent); !strings.Contains(w, substr) {
		t.Fatalf("missing-field warning = %q, want it to contain %q", w, substr)
	}
	if _, w := build(present); strings.Contains(w, substr) {
		t.Fatalf("present field must not warn %q, got %q", substr, w)
	}
}

func TestBuildFileExistsCheckPathRequired(t *testing.T) {
	// A path is required; when present it is carried onto the check.
	assertRequiredStringField(t,
		func(e map[string]any) (Check, string) { return buildFileExistsCheck(base{}, e) },
		map[string]any{"path": "/etc/hostname"},
		func(c Check) string { return c.(fileExistsCheck).path }, "/etc/hostname")
}

func TestRequireCheckPaths(t *testing.T) {
	path, errs := requireCheckPath(map[string]any{CheckKeyPath: "/run/example"}, CheckTypeFile)
	if path != "/run/example" || errs != "" {
		t.Fatalf("requireCheckPath = %q, %q", path, errs)
	}
	if _, errs := requireCheckPath(map[string]any{}, CheckTypeBinary); errs != "binary check requires a path" {
		t.Fatalf("missing scalar path warning = %q", errs)
	}
	paths, errs := requireCheckPaths(map[string]any{CheckKeyPath: []any{"/run/a", "/run/b"}}, CheckTypeSocket)
	if len(paths) != 2 || paths[0] != "/run/a" || paths[1] != "/run/b" || errs != "" {
		t.Fatalf("requireCheckPaths = %v, %q", paths, errs)
	}
	if _, errs := requireCheckPaths(map[string]any{}, CheckTypePidfile); errs != "pidfile check requires a path" {
		t.Fatalf("missing path list warning = %q", errs)
	}
}

func TestBuildLibrariesCheckBinaryRequired(t *testing.T) {
	assertRequiredStringField(t,
		func(e map[string]any) (Check, string) { return buildLibrariesCheck(base{}, e) },
		map[string]any{"binary": "/usr/bin/ssh"},
		func(c Check) string { return c.(librariesCheck).binary }, "/usr/bin/ssh")
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

func TestParseCommandExportsRejectsEmptyRegex(t *testing.T) {
	_, warn := parseCommandExports("api", map[string]any{
		"version": map[string]any{"regex": ""},
	})
	if !strings.Contains(warn, "regex must be non-empty") {
		t.Fatalf("warn = %q, want empty regex rejection", warn)
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

func TestParseStatusMatcherRejectsInvalidOperatorValues(t *testing.T) {
	if _, err := parseStatusMatcher(map[string]any{"op": "<", "value": "abc"}); err == nil {
		t.Fatal("non-numeric ordering value must be rejected")
	}
	if _, err := parseStatusMatcher(map[string]any{"op": "=~", "value": "["}); err == nil {
		t.Fatal("invalid regex value must be rejected")
	}
	if _, err := parseStatusMatcher(map[string]any{"op": "contains", "value": "2"}); err != nil {
		t.Fatalf("contains value should remain valid: %v", err)
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

// buildMetricForTest adapts buildMetricCheck to the assertRequiredField shape.
func buildMetricForTest(e map[string]any) (Check, string) { return buildMetricCheck(base{}, e, Deps{}) }

// buildICMPForTest adapts buildICMPCheck to the assertRequiredField shape.
func buildICMPForTest(e map[string]any) (Check, string) { return buildICMPCheck(base{}, e, Deps{}) }

func TestBuildMetricCheckNameRequired(t *testing.T) {
	// Missing name is rejected with a name-specific warning; a present name
	// does not trigger that warning.
	assertRequiredField(t, buildMetricForTest,
		map[string]any{"op": ">"},
		map[string]any{"name": "cpu", "op": ">"}, "requires a name")
}

func TestBuildMetricCheckOpRequired(t *testing.T) {
	assertRequiredField(t, buildMetricForTest,
		map[string]any{"name": "cpu"},
		map[string]any{"name": "cpu", "op": ">"}, "requires an op")
}

func TestBuildHdparmCheckDeviceRequired(t *testing.T) {
	assertRequiredField(t,
		func(e map[string]any) (Check, string) { return buildHdparmCheck(base{}, e, nil) },
		map[string]any{},
		map[string]any{"device": "/dev/sda", "read": map[string]any{"op": "<", "value": 1}},
		"requires a device")
}

func TestBuildSmartCheckDeviceRequired(t *testing.T) {
	assertRequiredField(t,
		func(e map[string]any) (Check, string) { return buildSmartCheck(base{}, e, nil) },
		map[string]any{},
		map[string]any{"device": "/dev/sda"}, "requires a device")
}

func TestBuildICMPCheckHostRequired(t *testing.T) {
	assertRequiredField(t, buildICMPForTest,
		map[string]any{"metric": "state", "expect": "up"},
		map[string]any{"host": "127.0.0.1", "metric": "state", "expect": "up"}, "requires a host")
}

func TestBuildICMPCheckCountPositive(t *testing.T) {
	// count == 0 is rejected (v <= 0, not v < 0); a positive count is accepted.
	assertRequiredField(t, buildICMPForTest,
		map[string]any{"host": "127.0.0.1", "count": 0, "metric": "state", "expect": "up"},
		map[string]any{"host": "127.0.0.1", "count": 3, "metric": "state", "expect": "up"},
		"positive integer")
}

func TestBuildICMPStateRequiresExpectOrOnChange(t *testing.T) {
	// metric=state with neither expect nor on:change is rejected; with an
	// expect it is accepted.
	assertRequiredField(t, buildICMPForTest,
		map[string]any{"host": "127.0.0.1", "metric": "state"},
		map[string]any{"host": "127.0.0.1", "metric": "state", "expect": "up"}, "requires expect")
}

func TestBuildICMPCheckConfiguresMetrics(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		check func(*icmpCheck) bool
	}{
		{
			name:  "state on change",
			entry: map[string]any{"host": "127.0.0.1", "metric": "state", "on": "change"},
			check: func(c *icmpCheck) bool { return c.onChange && c.expect == "" },
		},
		{
			name:  "latency threshold",
			entry: map[string]any{"host": "127.0.0.1", "metric": "latency", "threshold": map[string]any{"op": ">", "value": 10}},
			check: func(c *icmpCheck) bool { return c.hasThreshold && c.op == ">" && c.value == 10 },
		},
		{
			name:  "latency change",
			entry: map[string]any{"host": "127.0.0.1", "metric": "latency", "change": map[string]any{"delta": 5}},
			check: func(c *icmpCheck) bool { return c.hasChange && c.delta == 5 },
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			built, warn := buildICMPCheck(base{}, tc.entry, Deps{})
			if warn != "" {
				t.Fatalf("buildICMPCheck warning = %q", warn)
			}
			check := built.(*icmpCheck)
			if !tc.check(check) {
				t.Fatalf("icmp check = %+v", check)
			}
		})
	}
}

func TestBuildNetStateRequiresExpectOrOnChange(t *testing.T) {
	assertRequiredField(t,
		func(e map[string]any) (Check, string) { return buildNetCheck(base{}, e, Deps{}) },
		map[string]any{"interface": "eth0", "metric": "state"},
		map[string]any{"interface": "eth0", "metric": "state", "expect": "up"}, "requires expect")
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

func TestBuildNetErrorsCountersDefault(t *testing.T) {
	// metric=errors with no explicit counters defaults to rx/tx errors.
	c, w := buildNetCheck(base{}, map[string]any{"interface": "eth0", "metric": "errors", "delta": map[string]any{"op": ">", "value": "5"}}, Deps{})
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	got := c.(*netCheck).counters
	if len(got) != 2 || got[0] != "rx_errors" || got[1] != "tx_errors" {
		t.Fatalf("default counters = %v, want [rx_errors tx_errors]", got)
	}
	// Explicit counters are honored.
	c2, _ := buildNetCheck(base{}, map[string]any{"interface": "eth0", "metric": "errors", "counters": []any{"rx_dropped"}, "delta": map[string]any{"op": ">", "value": "5"}}, Deps{})
	if got := c2.(*netCheck).counters; len(got) != 1 || got[0] != "rx_dropped" {
		t.Fatalf("explicit counters = %v, want [rx_dropped]", got)
	}
}

func TestBuildMetricCheckScopeDefault(t *testing.T) {
	reader := func(scope, name string) (metrics.Reading, bool) { return metrics.Reading{}, false }
	// Absent scope defaults to "service".
	c, w := buildMetricCheck(base{}, map[string]any{"name": "cpu", "op": ">", "value": "90"}, Deps{Metrics: reader})
	if w != "" {
		t.Fatalf("unexpected warning: %q", w)
	}
	if got := c.(metricCheck).scope; got != "service" {
		t.Fatalf("default scope = %q, want service", got)
	}
	// An explicit scope is preserved.
	c2, _ := buildMetricCheck(base{}, map[string]any{"name": "cpu", "op": ">", "value": "90", "scope": "host"}, Deps{Metrics: reader})
	if got := c2.(metricCheck).scope; got != "host" {
		t.Fatalf("explicit scope = %q, want host", got)
	}
}
