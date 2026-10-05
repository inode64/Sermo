package config

import (
	"fmt"
	"strings"
	"testing"
)

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
