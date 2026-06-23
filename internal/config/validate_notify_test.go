package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNotifyDefault(t *testing.T) {
	if got := NotifyDefault(map[string]any{"notify": []any{"a", "b"}}); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("list = %v, want [a b]", got)
	}
	if got := NotifyDefault(map[string]any{"notify": "none"}); got != nil {
		t.Errorf("none scalar = %v, want nil", got)
	}
	if got := NotifyDefault(map[string]any{"notify": []any{"none"}}); got != nil {
		t.Errorf("none list = %v, want nil", got)
	}
	if got := NotifyDefault(map[string]any{}); got != nil {
		t.Errorf("absent = %v, want nil", got)
	}
}

func collect(fn func(add func(string, ...any))) []string {
	var issues []string
	fn(func(format string, args ...any) { issues = append(issues, fmt.Sprintf(format, args...)) })
	return issues
}

func TestValidateNotifiersRejectsReservedName(t *testing.T) {
	issues := collect(func(add func(string, ...any)) {
		validateNotifiers(map[string]any{
			"none": map[string]any{"type": "slack", "webhook": "https://hooks.example/x"},
		}, t.TempDir(), add)
	})
	if !strings.Contains(strings.Join(issues, "\n"), "reserved keyword") {
		t.Errorf("expected reserved-keyword issue, got: %v", issues)
	}
}

func TestValidateNotifySelection(t *testing.T) {
	defined := map[string]struct{}{"ops": {}, "oncall": {}}
	cases := []struct {
		name    string
		names   []string
		wantSub string // "" = expect no issue
	}{
		{"valid names", []string{"ops", "oncall"}, ""},
		{"none alone", []string{"none"}, ""},
		{"unknown", []string{"ghost"}, "references unknown notifier"},
		{"none mixed", []string{"none", "ops"}, "cannot be combined with notifier names"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := collect(func(add func(string, ...any)) {
				validateNotifySelection("notify", c.names, defined, add)
			})
			joined := strings.Join(issues, "\n")
			if c.wantSub == "" {
				if len(issues) != 0 {
					t.Errorf("expected no issues, got: %v", issues)
				}
			} else if !strings.Contains(joined, c.wantSub) {
				t.Errorf("expected %q, got: %v", c.wantSub, issues)
			}
		})
	}
}

func TestValidateBuiltinTTYNotifyReference(t *testing.T) {
	issues := validateRawGlobal(t, map[string]any{
		"notify": []any{"tty"},
	})
	var got []string
	for _, issue := range issues {
		got = append(got, issue.Msg)
	}
	if strings.Contains(strings.Join(got, "\n"), `unknown notifier "tty"`) {
		t.Fatalf("builtin tty notifier should validate without a notifier fragment: %v", got)
	}
}

func TestValidateRulesNotifyRefs(t *testing.T) {
	defined := map[string]struct{}{"ops": {}}
	tree := map[string]any{"rules": map[string]any{
		"alert-bad": map[string]any{
			"type":   "alert",
			"if":     map[string]any{"failed": map[string]any{"check": "http"}},
			"then":   map[string]any{"action": "alert", "message": "down"},
			"notify": []any{"ghost"},
		},
	}}
	issues := collect(func(add func(string, ...any)) { validateRules(tree, defined, add) })
	if !strings.Contains(strings.Join(issues, "\n"), "rules.alert-bad.notify references unknown notifier") {
		t.Errorf("expected rule notify ref issue, got: %v", issues)
	}
}

func TestValidateTeamsNotifier(t *testing.T) {
	issues := collect(func(add func(string, ...any)) {
		validateNotifiers(map[string]any{
			"ops-teams": map[string]any{"type": "teams", "webhook": "https://prod-01.westeurope.logic.azure.com/workflows/x"},
			"no-hook":   map[string]any{"type": "teams"},
		}, t.TempDir(), add)
	})
	joined := strings.Join(issues, "\n")
	if strings.Contains(joined, "ops-teams") {
		t.Errorf("valid teams notifier flagged: %v", issues)
	}
	if !strings.Contains(joined, "no-hook.webhook is required for a teams notifier") {
		t.Errorf("expected missing-webhook issue, got: %v", issues)
	}
}

func TestValidateNotifierTemplate(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "default-alert.yml"), []byte("subject: '{{ .Subject }}'\nbody: '{{ .Body }}'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	issues := collect(func(add func(string, ...any)) {
		validateNotifiers(map[string]any{
			"templated": map[string]any{
				"type":     "slack",
				"webhook":  "https://hooks.example/x",
				"template": "default-alert",
			},
			"missing": map[string]any{
				"type":     "slack",
				"webhook":  "https://hooks.example/x",
				"template": "ghost",
			},
		}, dir, add)
	})
	joined := strings.Join(issues, "\n")
	if strings.Contains(joined, "templated") {
		t.Errorf("valid template flagged: %v", issues)
	}
	if !strings.Contains(joined, `missing.template "ghost" is invalid`) {
		t.Errorf("expected missing-template issue, got: %v", issues)
	}
}
