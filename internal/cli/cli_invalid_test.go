package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/servicemgr"
)

var errNoConfigForInvalidTest = errors.New("no config")

func TestCLIRejectsUnknownFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "top level", args: []string{"--definitely-not-a-flag"}, want: "unknown flag --definitely-not-a-flag"},
		{name: "after command", args: []string{"status", "web", "--bogus"}, want: "unknown flag --bogus"},
		{name: "catalog command", args: []string{"apps", "--installed"}, want: "unknown flag --installed"},
		{name: "lock command", args: []string{"lock", "web", "--bad-lock-flag"}, want: "unknown flag --bad-lock-flag"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			app := App{Env: func(string) string { return "" }, Stdout: &bytes.Buffer{}, Stderr: &stderr}
			code := app.Run(context.Background(), tc.args)
			if code != exitUsage {
				t.Fatalf("Run(%v) exit = %d, want %d", tc.args, code, exitUsage)
			}
			if got := stderr.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("stderr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCLIRejectsMalformedCommands(t *testing.T) {
	global := writeActionConfig(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "unknown command", args: []string{"frobnicate"}, want: `unknown command "frobnicate"`},
		{name: "backend extra arg", args: []string{"backend", "extra"}, want: "backend takes no arguments"},
		{name: "status extra service", args: []string{"status", "web", "extra"}, want: "status takes exactly one service name"},
		{name: "is-active extra service", args: []string{"is-active", "web", "extra"}, want: "is-active takes exactly one service name"},
		{name: "restart extra service", args: []string{"restart", "web", "extra"}, want: "restart takes exactly one service name"},
		{name: "reload extra service", args: []string{"reload", "web", "extra"}, want: "reload takes exactly one service name"},
		{name: "preflight extra service", args: []string{"preflight", "web", "extra"}, want: "preflight takes exactly one service name"},
		{name: "locks extra service", args: []string{"locks", "web", "extra"}, want: "locks takes exactly one service name"},
		{name: "processes extra service", args: []string{"processes", "web", "extra"}, want: "processes takes exactly one service name"},
		{name: "monitor extra service", args: []string{"monitor", "web", "extra"}, want: "monitor takes exactly one service name"},
		{name: "unmonitor extra service", args: []string{"unmonitor", "web", "extra"}, want: "unmonitor takes exactly one service name"},
		{name: "apps bad selector", args: []string{"apps", "installed"}, want: "apps accepts only optional `all`"},
		{name: "apps extra selector", args: []string{"apps", "all", "extra"}, want: "apps accepts only optional `all`"},
		{name: "apps notify unsupported", args: []string{"apps", "--notify", "ops"}, want: "--notify is only supported by services"},
		{name: "libs bad selector", args: []string{"libs", "installed"}, want: "libs accepts only optional `all`"},
		{name: "services bad selector", args: []string{"services", "ghost"}, want: "services accepts only optional `all`"},
		{name: "patterns extra arg", args: []string{"patterns", "all"}, want: "patterns takes no arguments"},
		{name: "daemon missing subcommand", args: []string{"daemon"}, want: "daemon requires subcommand reload"},
		{name: "daemon bad subcommand", args: []string{"daemon", "nope"}, want: `unknown daemon subcommand "nope"`},
		{name: "mount missing target", args: []string{"mount"}, want: "mount requires a target"},
		{name: "mount list extra", args: []string{"mount", "list", "extra"}, want: "mount list takes no arguments"},
		{name: "mount status missing", args: []string{"mount", "status"}, want: "mount status requires exactly one mount name or path"},
		{name: "mount status extra", args: []string{"mount", "status", "/mnt/data", "extra"}, want: "mount status requires exactly one mount name or path"},
		{name: "mount target extra", args: []string{"mount", "/mnt/data", "extra"}, want: "mount takes exactly one target"},
		{name: "umount extra", args: []string{"umount", "/mnt/data", "extra"}, want: "umount takes exactly one mount name or path"},
		{name: "events list extra", args: []string{"events", "web", "extra"}, want: "events accepts at most one service name"},
		{name: "events clear extra", args: []string{"events", "clear", "extra"}, want: "events clear accepts only optional --before TIME"},
		{name: "activity extra", args: []string{"activity", "clear", "extra"}, want: "activity clear accepts only optional --before TIME"},
		{name: "activity bad subcommand", args: []string{"activity", "list"}, want: "activity supports only"},
		{name: "config validate service arg", args: []string{"config", "validate", "web"}, want: "config validate takes no service name"},
		{name: "sla extra", args: []string{"sla", "web", "extra"}, want: "sla accepts at most one service name"},
		{name: "state compact extra", args: []string{"state", "compact", "extra"}, want: "state supports only"},

		{name: "wizard extra", args: []string{"wizard", "service", "extra"}, want: "wizard accepts at most one assistant name"},
		{name: "lock acquire extra", args: []string{"lock", "acquire", "web", "extra", "--reason", "test", "--ttl", "1m"}, want: "lock acquire takes exactly one service name"},
		{name: "lock release extra", args: []string{"lock", "release", "web", "extra"}, want: "lock release takes exactly one service name"},
		{name: "lock wrap extra service", args: []string{"lock", "web", "extra", "--reason", "test", "--ttl", "1m", "--", "true"}, want: "lock wrap takes exactly one service name before --"},
		{name: "lock wrap missing command", args: []string{"lock", "web", "--reason", "test", "--ttl", "1m", "--"}, want: "requires a command after --"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			app := App{
				Env:        func(string) string { return "" },
				Stdout:     &bytes.Buffer{},
				Stderr:     &stderr,
				LoadConfig: config.Load,
			}
			args := append([]string{"--config", global}, tc.args...)
			code := app.Run(context.Background(), args)
			if code != exitUsage {
				t.Fatalf("Run(%v) exit = %d, want %d; stderr=%s", args, code, exitUsage, stderr.String())
			}
			if got := stderr.String(); !strings.Contains(got, tc.want) {
				t.Fatalf("stderr = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestConfiguredStatusRejectsUnknownService(t *testing.T) {
	global := writeActionConfig(t)
	var stderr bytes.Buffer
	app := App{
		LoadConfig: config.Load,
		Detector:   fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{status: servicemgr.ServiceStatus{
				Service: "ghost",
				Backend: servicemgr.BackendSystemd,
				Unit:    "ghost.service",
				Status:  servicemgr.StatusActive,
			}}, nil
		},
		Env:    func(string) string { return "" },
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
	}

	code := app.Run(context.Background(), []string{"--config", global, "status", "ghost"})
	if code != exitRuntimeError {
		t.Fatalf("status ghost exit = %d, want %d", code, exitRuntimeError)
	}
	if got := stderr.String(); !strings.Contains(got, `unknown service "ghost"`) {
		t.Fatalf("stderr = %q, want unknown service", got)
	}
}

func TestConfigValidateRejectsServiceArgumentBeforeLoadingConfig(t *testing.T) {
	var stderr bytes.Buffer
	app := App{
		Env:    func(string) string { return "" },
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		LoadConfig: func(string, ...config.Option) (*config.Config, error) {
			t.Fatal("config validate with a positional argument must fail before loading config")
			return nil, nil
		},
	}

	code := app.Run(context.Background(), []string{"--config", "/tmp/missing-sermo.yml", "config", "validate", "ghost"})
	if code != exitUsage {
		t.Fatalf("config validate ghost exit = %d, want %d", code, exitUsage)
	}
	if got := stderr.String(); !strings.Contains(got, "config validate takes no service name") {
		t.Fatalf("stderr = %q, want service argument usage error", got)
	}
}

func TestStatusStillAllowsDirectUnitWhenNoConfigLoads(t *testing.T) {
	var stdout bytes.Buffer
	app := App{
		LoadConfig: func(string, ...config.Option) (*config.Config, error) {
			return nil, errNoConfigForInvalidTest
		},
		Detector: fakeBackendDetector{detection: servicemgr.Detection{Backend: servicemgr.BackendSystemd}},
		NewManager: func(servicemgr.Backend) (servicemgr.Manager, error) {
			return fakeManager{status: servicemgr.ServiceStatus{
				Service: "nginx",
				Backend: servicemgr.BackendSystemd,
				Unit:    "nginx.service",
				Status:  servicemgr.StatusActive,
			}}, nil
		},
		Env:    func(string) string { return "" },
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
	}

	code := app.Run(context.Background(), []string{"status", "nginx"})
	if code != exitSuccess {
		t.Fatalf("status nginx exit = %d, want %d", code, exitSuccess)
	}
	if got := stdout.String(); !strings.Contains(got, "nginx state=started") {
		t.Fatalf("stdout = %q, want started status", got)
	}
}
