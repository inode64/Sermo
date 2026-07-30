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
		raw     any
		wantSub string // "" = expect no issue
	}{
		{"valid names", []string{"ops", "oncall"}, ""},
		{"scalar name", "ops", ""},
		{"none alone", []string{"none"}, ""},
		{"unknown", []string{"ghost"}, "references unknown notifier"},
		{"none mixed", []string{"none", "ops"}, "cannot be combined with notifier names"},
		{"invalid list item", []any{"ops", 7}, "must be a string or list of strings"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := collect(func(add func(string, ...any)) {
				validateNotifySelection("notify", c.raw, defined, add)
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
		"notify": []any{"tty", "wall"},
	})
	var got []string
	for _, issue := range issues {
		got = append(got, issue.Msg)
	}
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, `unknown notifier "tty"`) || strings.Contains(joined, `unknown notifier "wall"`) {
		t.Fatalf("builtin terminal notifiers should validate without notifier fragments: %v", got)
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

func TestValidateTelegramNotifier(t *testing.T) {
	cases := []struct {
		name    string
		entry   map[string]any
		wantSub string // "" = expect no issue
	}{
		{"valid minimal", map[string]any{"type": "telegram", "token": "t", "chat_id": "1"}, ""},
		{"valid full", map[string]any{"type": "telegram", "token": "t", "chat_id": "1", "parse_mode": "HTML", "silent": true, "message_thread_id": 7}, ""},
		{"empty token leaves notifier inactive", map[string]any{"type": "telegram", "chat_id": "1"}, ""},
		{"missing chat", map[string]any{"type": "telegram", "token": "t"}, "chat_id is required for a telegram notifier"},
		{"bad parse_mode", map[string]any{"type": "telegram", "token": "t", "chat_id": "1", "parse_mode": "rtf"}, "parse_mode must be one of"},
		{"non-bool silent", map[string]any{"type": "telegram", "token": "t", "chat_id": "1", "silent": "yes"}, "silent must be a boolean"},
		{"non-int thread", map[string]any{"type": "telegram", "token": "t", "chat_id": "1", "message_thread_id": "nope"}, "message_thread_id must be an integer"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := collect(func(add func(string, ...any)) {
				validateNotifiers(map[string]any{"tg": c.entry}, t.TempDir(), add)
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

func TestValidateTelegramBot(t *testing.T) {
	cases := []struct {
		name    string
		section map[string]any
		wantSub string // "" = expect no issue
	}{
		{"valid", map[string]any{"token": "t", "allowed_chats": []any{123, -1001234567890}}, ""},
		{"valid full", map[string]any{"token": "t", "allowed_chats": []any{1}, "poll_interval": "45s"}, ""},
		{"disabled skips checks", map[string]any{"enabled": false}, ""},
		{"empty token leaves bot inactive", map[string]any{"allowed_chats": []any{123}}, ""},
		{"enabled without token is optional", map[string]any{"enabled": true, "allowed_chats": []any{123}}, ""},
		{"no chats", map[string]any{"token": "t"}, "telegram_bot.allowed_chats must list at least one chat id"},
		{"bad interval", map[string]any{"token": "t", "allowed_chats": []any{1}, "poll_interval": "nope"}, "telegram_bot.poll_interval must be a positive duration"},
		{"non-bool enabled", map[string]any{"enabled": "yes", "token": "t", "allowed_chats": []any{1}}, "telegram_bot.enabled must be a boolean"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			issues := collect(func(add func(string, ...any)) {
				validateTelegramBot(map[string]any{SectionTelegramBot: c.section}, add)
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
