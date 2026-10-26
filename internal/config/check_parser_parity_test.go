package config

import (
	"fmt"
	"maps"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
)

func TestCheckFieldParsersAgreeWithBuilders(t *testing.T) {
	predicate := func(op string, value any) map[string]any { return map[string]any{"op": op, "value": value} }
	for _, tt := range []struct {
		name    string
		entry   map[string]any
		wantErr bool
	}{
		{"log", map[string]any{"type": "log", "path": "/missing/*.log", "regex": "error", "count": predicate(">", 3), "within": "5m"}, false},
		{"log relative path", map[string]any{"type": "log", "path": "relative.log", "regex": "error", "count": predicate(">", 3), "within": "5m"}, true},
		{"log regex", map[string]any{"type": "log", "path": "/missing/log", "regex": "(", "count": predicate(">", 3), "within": "5m"}, true},
		{"log zero window", map[string]any{"type": "log", "path": "/missing/log", "regex": "error", "count": predicate(">", 3), "within": "0s"}, true},
		{"log NaN", map[string]any{"type": "log", "path": "/missing/log", "regex": "error", "count": predicate(">", "NaN"), "within": "5m"}, true},
		{"count", map[string]any{"type": "count", "path": "/missing", "count": predicate(">", 3)}, false},
		{"count invalid mapping", map[string]any{"type": "count", "path": "/missing", "count": "3"}, true},
		{"count mixed modes", map[string]any{"type": "count", "path": "/missing", "count": predicate(">", 3), "op": "<", "value": 1}, true},
		{"count boolean", map[string]any{"type": "count", "path": "/missing", "recursive": "yes", "count": predicate(">", 3)}, true},
		{"count flat threshold", map[string]any{"type": "count", "path": "/missing", "op": ">", "value": 3}, true},
		{"count growth", map[string]any{"type": "count", "path": "/missing", "delta": predicate(">", 3), "within": "2m"}, false},
		{"count infinite delta", map[string]any{"type": "count", "path": "/missing", "delta": predicate(">", "+Inf"), "within": "2m"}, true},
		{"size", map[string]any{"type": "size", "path": "/missing", "grow_by": "1G", "within": "1h"}, false},
		{"size overflow", map[string]any{"type": "size", "path": "/missing", "grow_by": "8388608T", "within": "1h"}, true},
		{"size zero window", map[string]any{"type": "size", "path": "/missing", "grow_by": "1G", "within": "0s"}, true},
		{"size boolean", map[string]any{"type": "size", "path": "/missing", "grow_by": "1G", "within": "1h", "include_hidden": "yes"}, true},
		{"load infinite", map[string]any{"type": "load", "load1": predicate(">", "+Inf")}, true},
		{"storage bytes", map[string]any{"type": "storage", "path": "/missing", "free_bytes": predicate("<", "1G")}, false},
		{"storage unitless", map[string]any{"type": "storage", "path": "/missing", "free_bytes": predicate("<", 1024)}, true},
		{"storage percent", map[string]any{"type": "storage", "path": "/missing", "used_pct": predicate(">", "95%")}, false},
		{"storage invalid percent", map[string]any{"type": "storage", "path": "/missing", "used_pct": predicate(">", 101)}, true},
		{"replication", map[string]any{"type": "replication", "user": "probe", "behind": predicate("<", 30)}, false},
		{"replication defaults_file", map[string]any{"type": "replication", "defaults_file": "/root/.my.cnf"}, false},
		{"replication without account", map[string]any{"type": "replication"}, true},
		{"replication invalid mapping", map[string]any{"type": "replication", "user": "probe", "behind": "30"}, true},
		{"replication NaN", map[string]any{"type": "replication", "user": "probe", "behind": predicate("<", "NaN")}, true},
		{"replication pending", map[string]any{"type": "replication", "user": "probe", "source_defaults_file": "/etc/source.cnf", "pending_bytes": predicate("<", "1G")}, false},
		{"replication source only", map[string]any{"type": "replication", "user": "probe", "source_defaults_file": "/etc/source.cnf"}, false},
		{"replication pending without source", map[string]any{"type": "replication", "user": "probe", "pending_bytes": predicate("<", "1G")}, true},
		{"replication pending unitless", map[string]any{"type": "replication", "user": "probe", "source_defaults_file": "/etc/source.cnf", "pending_bytes": predicate("<", 1024)}, true},
		{"icmp threshold", map[string]any{"type": "icmp", "host": "192.0.2.1", "metric": "latency", "threshold": predicate(">", 100)}, false},
		{"icmp mixed modes", map[string]any{"type": "icmp", "host": "192.0.2.1", "metric": "latency", "threshold": predicate(">", 100), "change": map[string]any{"delta": 10}}, true},
		{"icmp invalid change", map[string]any{"type": "icmp", "host": "192.0.2.1", "metric": "latency", "change": "10"}, true},
		{"icmp infinite delta", map[string]any{"type": "icmp", "host": "192.0.2.1", "metric": "latency", "change": map[string]any{"delta": "+Inf"}}, true},
		{"oom invalid delta", map[string]any{"type": "oom", "delta": predicate(">", "NaN")}, true},
		{"command without analyze", map[string]any{"type": "command", "command": []any{"/test/tool"}}, false},
		{"command empty analyze", map[string]any{"type": "command", "command": []any{"/test/tool"}, "analyze": map[string]any{}}, false},
		{"command null analyze", map[string]any{"type": "command", "command": []any{"/test/tool"}, "analyze": nil}, true},
		{"command invalid analyze", map[string]any{"type": "command", "command": []any{"/test/tool"}, "analyze": "rules"}, true},
		{"command empty analysis rules", map[string]any{"type": "command", "command": []any{"/test/tool"}, "analyze": map[string]any{"rules": []any{}}}, false},
		{"command null analysis rules", map[string]any{"type": "command", "command": []any{"/test/tool"}, "analyze": map[string]any{"rules": nil}}, true},
		{"command invalid analysis rules", map[string]any{"type": "command", "command": []any{"/test/tool"}, "analyze": map[string]any{"rules": "error"}}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			typ := tt.entry["type"].(string)
			for _, watch := range []bool{false, true} {
				var diagnostics []string
				add := func(format string, args ...any) { diagnostics = append(diagnostics, fmt.Sprintf(format, args...)) }
				validate := validateSingleShotCheckFields
				if watch {
					validate = validateWatchableCheck
				}
				if !validate("target.probe", typ, tt.entry, "/run/sermo/locks", add) {
					t.Fatal("check type not registered")
				}
				if (len(diagnostics) != 0) != tt.wantErr {
					t.Fatalf("watch=%v validation = %v, wantErr %v", watch, diagnostics, tt.wantErr)
				}
			}
			built, issues := checks.BuildWithIssues(map[string]any{"probe": maps.Clone(tt.entry)}, checks.Deps{DefaultTimeout: time.Second})
			if (len(issues) != 0) != tt.wantErr || (len(built) == 0) != tt.wantErr {
				t.Fatalf("BuildWithIssues = %d checks, %v; wantErr %v", len(built), issues, tt.wantErr)
			}
		})
	}
}

func TestSharedAnalyzerReportsAllInvalidFields(t *testing.T) {
	entry := map[string]any{"analyze": map[string]any{"rules": []any{
		map[string]any{"id": "bad", "severity": "fatal", "stream": "syslog", "match": "("},
		map[string]any{"id": "bad", "severity": "warning"},
		map[string]any{"severity": "warning", "match": "error"},
		"invalid",
	}}}
	var diagnostics []string
	validateAnalyze("checks.probe", entry, func(format string, args ...any) {
		diagnostics = append(diagnostics, fmt.Sprintf(format, args...))
	})
	joined := strings.Join(diagnostics, "\n")
	for _, want := range []string{"severity must be", "stream must be", "invalid regex", "duplicate rule id", "missing a match", "missing an id", "must be a mapping"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in %s", want, joined)
		}
	}
	for _, diagnostic := range diagnostics {
		if !strings.HasPrefix(diagnostic, "checks.probe.analyze ") {
			t.Errorf("missing configuration path: %s", diagnostic)
		}
	}
}

func TestSharedLogParserReportsAllInvalidFields(t *testing.T) {
	var diagnostics []string
	singleShotCheckValidators[checks.CheckTypeLog]("checks.probe", map[string]any{"path": "relative", "regex": "(", "count": map[string]any{"op": "bad", "value": "NaN"}}, "", func(format string, args ...any) {
		diagnostics = append(diagnostics, fmt.Sprintf(format, args...))
	})
	joined := strings.Join(diagnostics, "\n")
	for _, want := range []string{"path must be absolute", "regex is invalid", "invalid op", "finite number", "within is required"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %s", want, joined)
		}
	}
	for _, diagnostic := range diagnostics {
		if !strings.HasPrefix(diagnostic, "checks.probe ") {
			t.Fatalf("missing configuration path: %s", diagnostic)
		}
	}
}
