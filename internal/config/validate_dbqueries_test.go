package config

import (
	"fmt"
	"strings"
	"testing"

	"sermo/internal/checks"
)

func TestDBQueriesResourceValidation(t *testing.T) {
	for _, service := range []bool{false, true} {
		for _, tt := range []struct {
			name  string
			check map[string]any
			kill  bool
			want  string
		}{
			{name: "resource only", check: map[string]any{"cpu_thread": map[string]any{"op": ">", "value": "90%"}}},
			{name: "duration and memory", check: map[string]any{"min_duration": "30s", "memory": map[string]any{"op": ">", "value": "256MiB"}}},
			{name: "bad CPU range", check: map[string]any{"cpu": map[string]any{"op": ">", "value": "101%"}}, want: "cpu value"},
			{name: "bad operator", check: map[string]any{"cpu_thread": map[string]any{"op": "above", "value": "90%"}}, want: "cpu_thread has an invalid op"},
			{name: "bad memory", check: map[string]any{"memory": map[string]any{"op": ">", "value": "50%"}}, want: "memory value"},
			{name: "no thresholds", check: map[string]any{}, want: "requires min_duration"},
			{name: "unknown field", check: map[string]any{"min_duration": "30s", "cpu_total": map[string]any{"op": ">", "value": 20}}, want: "cpu_total is not supported"},
			{name: "unsafe automatic kill", check: map[string]any{"min_duration": "30s", "cpu_thread": map[string]any{"op": ">", "value": 90}}, kill: true, want: "kill_query"},
		} {
			t.Run(fmt.Sprintf("service=%v/%s", service, tt.name), func(t *testing.T) {
				tt.check[checks.CheckKeyType] = checks.CheckTypeDBQueries
				tt.check[checks.CheckKeyEngine] = "mariadb"
				entry := map[string]any{"check": tt.check}
				if tt.kill {
					entry["then"] = map[string]any{"kill_query": map[string]any{"after": "5m", "users": []any{"app"}}}
					entry["policy"] = map[string]any{"cooldown": "10m"}
				}
				var issues []string
				validateDBQueriesWatch("sql", tt.check, entry, service, nil, func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) })
				got := strings.Join(issues, "\n")
				if tt.want == "" && got != "" || tt.want != "" && !strings.Contains(got, tt.want) {
					t.Fatalf("validation = %q, want %q", got, tt.want)
				}
			})
		}
	}
}

func TestDBQueriesPolicyRequiresCooldownOnlyWithKill(t *testing.T) {
	for _, tt := range []struct {
		name    string
		entry   map[string]any
		hasKill bool
		want    string
	}{
		{name: "monitor only", entry: map[string]any{}},
		{name: "missing", entry: map[string]any{}, hasKill: true, want: "policy.cooldown"},
		{name: "malformed", entry: map[string]any{"policy": "5m"}, hasKill: true, want: "policy.cooldown"},
		{name: "empty", entry: map[string]any{"policy": map[string]any{}}, hasKill: true, want: "policy.cooldown"},
		{name: "zero", entry: map[string]any{"policy": map[string]any{"cooldown": "0s"}}, hasKill: true, want: "policy.cooldown"},
		{name: "negative", entry: map[string]any{"policy": map[string]any{"cooldown": "-1m"}}, hasKill: true, want: "policy.cooldown"},
		{name: "invalid", entry: map[string]any{"policy": map[string]any{"cooldown": "soon"}}, hasKill: true, want: "policy.cooldown"},
		{name: "paced", entry: map[string]any{"policy": map[string]any{"cooldown": "5m"}}, hasKill: true},
		{name: "no kill", entry: map[string]any{"policy": map[string]any{"cooldown": "5m"}}, want: "only valid with then.kill_query"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var errs []string
			validateDBQueriesPolicy("long", tt.entry, tt.hasKill, func(format string, args ...any) {
				errs = append(errs, fmt.Sprintf(format, args...))
			})
			if tt.want == "" {
				if len(errs) != 0 {
					t.Fatalf("unexpected errors: %v", errs)
				}
			} else if len(errs) != 1 || !strings.Contains(errs[0], tt.want) {
				t.Fatalf("errors = %v, want one error containing %q", errs, tt.want)
			}
		})
	}
}
