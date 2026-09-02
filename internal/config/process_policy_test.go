package config

import (
	"slices"
	"testing"
)

func TestParseProcessPolicyAllows(t *testing.T) {
	t.Run("valid entries are sorted and compiled", func(t *testing.T) {
		allows, issues := ParseProcessPolicyAllows(map[string]any{
			"worker": map[string]any{"exe": "/usr/bin/worker"},
			"main":   map[string]any{"exe": "/usr/bin/server", "cmd": "^server --foreground$"},
		})
		if len(issues) != 0 {
			t.Fatalf("issues = %v", issues)
		}
		if len(allows) != 2 || allows[0].Name != "main" || allows[1].Name != "worker" {
			t.Fatalf("allows = %+v", allows)
		}
		if allows[0].Cmd == nil || !allows[0].Cmd.MatchString("server --foreground") || allows[1].Cmd != nil {
			t.Fatalf("compiled commands = %+v", allows)
		}
	})

	tests := []struct {
		name string
		raw  any
		want []string
	}{
		{
			name: "missing mapping",
			want: []string{"process_policy check.allow is required and must be a non-empty mapping"},
		},
		{
			name: "entry is not a mapping",
			raw:  map[string]any{"main": "invalid"},
			want: []string{"process_policy check.allow.main must be a mapping"},
		},
		{
			name: "invalid fields are all reported",
			raw: map[string]any{"main": map[string]any{
				"exe":   "../server",
				"cmd":   "(",
				"extra": true,
			}},
			want: []string{
				"process_policy check.allow.main.extra is not supported",
				"process_policy check.allow.main.exe must be a clean absolute resolved executable path",
				"process_policy check.allow.main.cmd must be anchored with ^ and $",
			},
		},
		{
			name: "empty command",
			raw:  map[string]any{"main": map[string]any{"exe": "/usr/bin/server", "cmd": ""}},
			want: []string{"process_policy check.allow.main.cmd must be a non-empty anchored RE2 expression"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, issues := ParseProcessPolicyAllows(tt.raw)
			got := make([]string, 0, len(issues))
			for _, issue := range issues {
				got = append(got, issue.Error())
			}
			if len(got) < len(tt.want) || !slices.Equal(got[:len(tt.want)], tt.want) {
				t.Fatalf("issues = %q, want prefix %q", got, tt.want)
			}
			if tt.name == "invalid fields are all reported" && len(got) != 4 {
				t.Fatalf("issues = %q, want unsupported, exe, anchor, and regexp errors", got)
			}
		})
	}
}
