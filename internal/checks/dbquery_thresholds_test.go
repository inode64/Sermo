package checks

import (
	"strings"
	"testing"
)

func TestDBQueryResourceConfig(t *testing.T) {
	for _, tt := range []struct {
		name  string
		field string
		value any
		want  string
	}{
		{name: "total CPU", field: "cpu", value: "12.5%"},
		{name: "thread CPU", field: "cpu_thread", value: 90},
		{name: "connection memory", field: "memory", value: "256MiB"},
		{name: "CPU over range", field: "cpu", value: "101%", want: "percentage"},
		{name: "thread negative", field: "cpu_thread", value: -1, want: "percentage"},
		{name: "CPU not finite", field: "cpu", value: ".nan", want: "percentage"},
		{name: "CPU byte size", field: "cpu_thread", value: "90MiB", want: "percentage"},
		{name: "memory needs units", field: "memory", value: 256, want: "size suffix"},
		{name: "memory percent", field: "memory", value: "20%", want: "size suffix"},
		{name: "missing value", field: "cpu", want: "percentage"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseDBQueryConfig(map[string]any{
				"engine": "mariadb", tt.field: map[string]any{"op": ">", "value": tt.value},
			})
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want %q", err, tt.want)
				}
				return
			}
			if err != nil || !cfg.HasResourceThresholds() || cfg.MinDuration != 0 {
				t.Fatalf("resource-only config = %+v, %v", cfg, err)
			}
			if _, err := ParseDBQueryKill(map[string]any{"after": "30m", "users": []any{"report"}}, cfg); err == nil || !strings.Contains(err.Error(), "resource thresholds") {
				t.Fatalf("automatic resource kill must be rejected: %v", err)
			}
		})
	}
}

func TestDBQueryResourceMatches(t *testing.T) {
	cfg, err := ParseDBQueryConfig(map[string]any{
		"engine": "mariadb", "min_duration": "30s", "exclude_users": []any{"backup"},
		"cpu":        map[string]any{"op": ">", "value": "20%"},
		"cpu_thread": map[string]any{"op": ">", "value": "90%"},
		"memory":     map[string]any{"op": ">=", "value": "256MiB"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		name    string
		query   DBQuery
		matched bool
		ready   bool
		want    string
	}{
		{name: "one saturated thread", query: DBQuery{ElapsedSeconds: 30, CPU: 12.5, CPUThread: 100, CPUReady: true}, matched: true, ready: true, want: "cpu_thread 100% > 90%"},
		{name: "total CPU", query: DBQuery{ElapsedSeconds: 30, CPU: 25, CPUReady: true}, matched: true, ready: true, want: "cpu 25% > 20%"},
		{name: "memory without CPU", query: DBQuery{ElapsedSeconds: 30, MemoryBytes: 256 << 20, MemoryReady: true}, matched: true, ready: true, want: "memory 256 MiB >= 256 MiB"},
		{name: "too young", query: DBQuery{ElapsedSeconds: 29, CPU: 100, CPUThread: 100, CPUReady: true}, ready: true},
		{name: "excluded", query: DBQuery{User: "backup", ElapsedSeconds: 60, CPU: 100, CPUThread: 100, CPUReady: true}, ready: true},
		{name: "idle measured zero", query: DBQuery{ElapsedSeconds: 30, CPUReady: true, MemoryReady: true}, ready: true},
		{name: "one missing reading", query: DBQuery{ElapsedSeconds: 30, CPUReady: true}},
		{name: "all readings missing", query: DBQuery{ElapsedSeconds: 30}},
		{name: "equal CPU is not greater", query: DBQuery{ElapsedSeconds: 30, CPU: 20, CPUThread: 90, CPUReady: true, MemoryReady: true}, ready: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			matched, ready := cfg.QueryMatches(tt.query)
			if matched != tt.matched || ready != tt.ready {
				t.Fatalf("match = (%v, %v), want (%v, %v)", matched, ready, tt.matched, tt.ready)
			}
			if tt.want != "" && cfg.ResourceMatchSummary(tt.query) != tt.want {
				t.Fatalf("summary = %q, want %q", cfg.ResourceMatchSummary(tt.query), tt.want)
			}
		})
	}
}
