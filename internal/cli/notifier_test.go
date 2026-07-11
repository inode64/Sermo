package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/notify"
)

func TestNotifierTestSendsMarkedMessage(t *testing.T) {
	notifier := &fakeReportNotifier{name: "ops"}
	var stdout bytes.Buffer
	app := App{
		Env:        func(string) string { return "" },
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
		LoadConfig: func(string, ...config.Option) (*config.Config, error) { return &config.Config{}, nil },
		BuildNotifiers: func(*config.Config) (map[string]notify.Notifier, []string) {
			return map[string]notify.Notifier{"ops": notifier}, nil
		},
	}
	if code := app.Run(context.Background(), []string{"notifier", "test", "ops"}); code != exitSuccess {
		t.Fatalf("notifier test exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "test notification sent to ops") {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if notifier.msg.Subject != notify.TestSubject || notifier.msg.Fields[notify.TestField] != "true" {
		t.Fatalf("test message = %+v", notifier.msg)
	}
}

func TestNotifierTestRejectsUnknownOrDisabledNotifier(t *testing.T) {
	var stderr bytes.Buffer
	app := App{
		Env:        func(string) string { return "" },
		Stdout:     &bytes.Buffer{},
		Stderr:     &stderr,
		LoadConfig: func(string, ...config.Option) (*config.Config, error) { return &config.Config{}, nil },
		BuildNotifiers: func(*config.Config) (map[string]notify.Notifier, []string) {
			return map[string]notify.Notifier{}, nil
		},
	}
	if code := app.Run(context.Background(), []string{"notifier", "test", "muted"}); code != exitUsage {
		t.Fatalf("notifier test exit = %d", code)
	}
	if !strings.Contains(stderr.String(), `unknown or disabled notifier "muted"`) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
