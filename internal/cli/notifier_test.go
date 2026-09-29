package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

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

type deadlineNotifier struct {
	fakeReportNotifier
	remaining time.Duration
}

func (d *deadlineNotifier) Send(ctx context.Context, msg notify.Message) error {
	if deadline, ok := ctx.Deadline(); ok {
		d.remaining = time.Until(deadline)
	}
	return d.fakeReportNotifier.Send(ctx, msg)
}

// An SMTP or webhook test often needs more than the 2s probe budget; like the
// Web UI, the CLI test uses engine.default_timeout unless --timeout is given.
func TestNotifierTestUsesEngineDefaultTimeout(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		min  time.Duration
		max  time.Duration
	}{
		{name: "engine default_timeout", min: 20 * time.Second, max: 25 * time.Second},
		{name: "explicit --timeout", args: []string{"--timeout", "4s"}, min: 3 * time.Second, max: 4 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notifier := &deadlineNotifier{name: "ops"}
			cfg := &config.Config{Global: config.Global{Raw: map[string]any{
				config.SectionEngine: map[string]any{config.EngineKeyDefaultTimeout: "25s"},
			}}}
			app := App{
				Env:        func(string) string { return "" },
				Stdout:     &bytes.Buffer{},
				Stderr:     &bytes.Buffer{},
				LoadConfig: func(string, ...config.Option) (*config.Config, error) { return cfg, nil },
				BuildNotifiers: func(*config.Config) (map[string]notify.Notifier, []string) {
					return map[string]notify.Notifier{"ops": notifier}, nil
				},
			}
			if code := app.Run(context.Background(), append(tc.args, "notifier", "test", "ops")); code != exitSuccess {
				t.Fatalf("notifier test exit = %d", code)
			}
			if notifier.remaining < tc.min || notifier.remaining > tc.max {
				t.Fatalf("send deadline = %s, want within [%s, %s]", notifier.remaining, tc.min, tc.max)
			}
		})
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
