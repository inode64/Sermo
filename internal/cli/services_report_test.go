package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/config"
	"sermo/internal/notify"
)

type fakeReportNotifier struct {
	name string
	msg  notify.Message
}

func (f *fakeReportNotifier) Name() string { return f.name }

func (f *fakeReportNotifier) Type() string { return "email" }

func (f *fakeReportNotifier) Send(_ context.Context, msg notify.Message) error {
	f.msg = msg
	return nil
}

func TestServicesReportMessageHTML(t *testing.T) {
	reports := []appinspect.Report{
		{DisplayName: "Nginx", VersionShort: "1.28.0", Installed: true, OK: true, Status: "ok"},
		{DisplayName: "<Bad Service>", Installed: true, OK: false, Status: "error: boom"},
		{DisplayName: "Missing", Installed: false, Status: "not installed"},
	}
	msg := servicesReportMessage(reports, true, time.Date(2026, 6, 17, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(msg.Subject, "1 ok") || !strings.Contains(msg.Subject, "1 issue") {
		t.Fatalf("subject = %q", msg.Subject)
	}
	if !strings.Contains(msg.Body, "Nginx") || !strings.Contains(msg.Body, "Not installed: 1") {
		t.Fatalf("plain body missing report details:\n%s", msg.Body)
	}
	if !strings.Contains(msg.HTML, "Service catalog health") || !strings.Contains(msg.HTML, "&lt;Bad Service&gt;") {
		t.Fatalf("HTML body missing layout or escaping:\n%s", msg.HTML)
	}
	if strings.Contains(msg.HTML, "<Bad Service>") {
		t.Fatalf("HTML body did not escape service name:\n%s", msg.HTML)
	}
}

func TestServicesCommandNotifySendsReport(t *testing.T) {
	root := t.TempDir()
	catalogDir := filepath.Join(root, "catalog")
	catalogServicesDir := filepath.Join(catalogDir, "services")
	binDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(catalogServicesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(binDir, "nginx")
	if err := os.WriteFile(binary, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(catalogServicesDir, "nginx.yml"), []byte(`
kind: daemon
name: nginx
display_name: "Nginx"
variables:
  binary: `+binary+`
preflight: { binary: { type: binary, path: "`+binary+`" } }
`), 0o644); err != nil {
		t.Fatal(err)
	}
	global := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(global, []byte(`
paths:
  catalog: [`+catalogDir+`]
  runtime: /run/sermo
defaults: { policy: { cooldown: 5m } }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	notifier := &fakeReportNotifier{name: "ops"}
	var stdout bytes.Buffer
	app := App{
		Env:    func(string) string { return "" },
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		BuildNotifiers: func(*config.Config) (map[string]notify.Notifier, []string) {
			return map[string]notify.Notifier{"ops": notifier}, nil
		},
	}
	if code := app.Run(context.Background(), []string{"--config", global, "services", "--notify", "ops"}); code != exitSuccess {
		t.Fatalf("services --notify exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "sent services report to ops") {
		t.Fatalf("stdout missing sent confirmation:\n%s", stdout.String())
	}
	if notifier.msg.Subject == "" || notifier.msg.HTML == "" || !strings.Contains(notifier.msg.Body, "Nginx") {
		t.Fatalf("notifier message = %+v", notifier.msg)
	}
}

func TestSelectServicesReportNotifiers(t *testing.T) {
	ops := &fakeReportNotifier{name: "ops"}
	pager := &fakeReportNotifier{name: "pager"}
	registry := map[string]notify.Notifier{"pager": pager, "ops": ops}
	selected, names, err := selectServicesReportNotifiers([]string{"all"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 || strings.Join(names, ",") != "ops,pager" {
		t.Fatalf("all selected names=%v selected=%d", names, len(selected))
	}
	if _, _, err := selectServicesReportNotifiers([]string{"ghost"}, registry); err == nil {
		t.Fatal("unknown notifier must fail")
	}
}
