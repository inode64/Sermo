package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/appinspect"
	"sermo/internal/config"
	"sermo/internal/notify"
)

type fakeReportNotifier struct {
	name string
	typ  string
	msg  notify.Message
	err  error
}

func (f *fakeReportNotifier) Name() string { return f.name }

func (f *fakeReportNotifier) Type() string {
	if f.typ != "" {
		return f.typ
	}
	return "email"
}

func (f *fakeReportNotifier) Send(_ context.Context, msg notify.Message) error {
	f.msg = msg
	return f.err
}

func reportNotifierNames(notifiers []notify.Notifier) string {
	names := make([]string, 0, len(notifiers))
	for _, n := range notifiers {
		names = append(names, n.Name())
	}
	return strings.Join(names, ",")
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
engine:
  runtime: /run/sermo
defaults: { policy: { cooldown: 5m } }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	notifier := &fakeReportNotifier{name: "ops"}
	var stdout bytes.Buffer
	app := hermeticApp(t, App{
		Env:        func(string) string { return "" },
		Stdout:     &stdout,
		Stderr:     &bytes.Buffer{},
		LoadConfig: testLoadConfigWithCatalog(catalogDir),
		BuildReportNotifiers: func(*config.Config) (map[string]notify.Notifier, []string) {
			return map[string]notify.Notifier{"ops": notifier}, nil
		},
	})
	if code := app.Run(context.Background(), []string{"--config", global, "services", "catalog", "--notify", "ops"}); code != exitSuccess {
		t.Fatalf("services --notify exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "sent services report to ops") {
		t.Fatalf("stdout missing sent confirmation:\n%s", stdout.String())
	}
	if notifier.msg.Subject == "" || notifier.msg.HTML == "" || !strings.Contains(notifier.msg.Body, "Nginx") {
		t.Fatalf("notifier message = %+v", notifier.msg)
	}
}

// One notifier that cannot deliver (a wall with nobody logged in) must not cost
// the others their report, nor the operator the listing, and the exit status
// must still say something failed.
func TestServicesNotifyTriesEveryNotifier(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "configured services", args: []string{"services"}, want: "no configured services"},
		{name: "catalog", args: []string{"services", "catalog"}, want: "no installed services"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			global := filepath.Join(root, "sermo.yml")
			mustWrite(t, global, "defaults: { policy: { cooldown: 5m } }\n")
			var failing, working *fakeReportNotifier
			run := func(extra ...string) (int, string, string) {
				failing = &fakeReportNotifier{name: "a-wall", typ: notify.TypeWall, err: errors.New("no active terminal sessions")}
				working = &fakeReportNotifier{name: "b-ops"}
				var stdout, stderr bytes.Buffer
				app := hermeticApp(t, App{
					Env: func(string) string { return "" }, Stdout: &stdout, Stderr: &stderr,
					BuildReportNotifiers: func(*config.Config) (map[string]notify.Notifier, []string) {
						return map[string]notify.Notifier{failing.name: failing, working.name: working}, nil
					},
				})
				args := append([]string{"--config", global, "--notify", "all"}, tc.args...)
				code := app.Run(context.Background(), append(args, extra...))
				return code, stdout.String(), stderr.String()
			}

			code, stdout, stderr := run()
			if code != exitRuntimeError {
				t.Fatalf("exit = %d, want %d", code, exitRuntimeError)
			}
			if working.msg.Subject == "" {
				t.Fatal("the notifier after the failed one received nothing")
			}
			if !strings.Contains(stdout, tc.want) || !strings.Contains(stdout, "sent services report to b-ops") {
				t.Fatalf("stdout = %q, want the listing and the delivery", stdout)
			}
			if !strings.Contains(stderr, "a-wall") || !strings.Contains(stderr, "no active terminal sessions") {
				t.Fatalf("stderr = %q, want the failed notifier and its reason", stderr)
			}

			code, stdout, stderr = run("--json")
			if code != exitRuntimeError {
				t.Fatalf("--json exit = %d, want %d", code, exitRuntimeError)
			}
			var got struct {
				Services []any                   `json:"services"`
				Notified []string                `json:"notified"`
				Failed   []servicesReportFailure `json:"notify_failed"`
			}
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("--json stdout %q: %v", stdout, err)
			}
			if !slices.Equal(got.Notified, []string{"b-ops"}) || len(got.Failed) != 1 ||
				got.Failed[0].Notifier != "a-wall" || !strings.Contains(got.Failed[0].Error, "no active terminal sessions") {
				t.Fatalf("--json outcome = %+v", got)
			}
			if stderr != "" {
				t.Fatalf("--json stderr = %q, want every outcome in the document", stderr)
			}
		})
	}
}

func TestSelectServicesReportNotifiers(t *testing.T) {
	ops := &fakeReportNotifier{name: "ops"}
	pager := &fakeReportNotifier{name: "pager"}
	registry := map[string]notify.Notifier{"pager": pager, "ops": ops}
	selected, err := selectServicesReportNotifiers([]string{"all"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if names := reportNotifierNames(selected); names != "ops,pager" {
		t.Fatalf("all selected = %s", names)
	}
	if _, err := selectServicesReportNotifiers([]string{"ghost"}, registry); err == nil {
		t.Fatal("unknown notifier must fail")
	}
}

func TestSelectServicesReportNotifiersWallSuppressesTTY(t *testing.T) {
	registry := map[string]notify.Notifier{
		"tty":  &fakeReportNotifier{name: "tty", typ: "tty"},
		"wall": &fakeReportNotifier{name: "wall", typ: "wall"},
	}
	selected, err := selectServicesReportNotifiers([]string{"tty", "wall"}, registry)
	if err != nil {
		t.Fatal(err)
	}
	if names := reportNotifierNames(selected); names != "wall" {
		t.Fatalf("selected = %s, want wall only", names)
	}
}
