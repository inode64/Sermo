package assist

import (
	"strings"
	"testing"
)

func serviceTestEnv() Env {
	return Env{
		Backend:      "openrc",
		ServiceNames: map[string]struct{}{"redis": {}}, // redis already configured -> skipped if chosen
		Daemons: func() ([]DaemonCandidate, error) {
			return []DaemonCandidate{
				{Name: "nginx", Title: "Nginx", Unit: "nginx", Port: 80, UnitPresent: true, ConfigPaths: []string{"/etc/nginx/nginx.conf"}},
				{Name: "named", Title: "BIND", Unit: "named", Port: 53, UnitPresent: true, PortListening: true},
			}, nil
		},
	}
}

func TestServiceAssistant(t *testing.T) {
	// Select nginx (1) with a port override; no pidfile; monitor enabled, inherit
	// interval. The wizard never asks for a name — it is the candidate's.
	script := strings.Join([]string{
		"1",    // MultiChoose -> nginx
		"8080", // port override
		"",     // pidfile: skip (candidate has none detected)
		"1",    // monitor state: enabled
		"",     // interval: inherit
	}, "\n") + "\n"

	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, serviceTestEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	svc, ok := res.Services["nginx"].(map[string]any)
	if !ok {
		t.Fatalf("expected service nginx, got %v", res.Services)
	}
	if svc["uses"] != "nginx" || svc["enabled"] != true {
		t.Fatalf("body = %v", svc)
	}
	if svc["monitor"] != "enabled" {
		t.Fatalf("monitor = %v, want enabled", svc["monitor"])
	}
	vars, _ := svc["variables"].(map[string]any)
	if vars == nil || vars["port"] != 8080 {
		t.Fatalf("expected port override 8080, got %v", svc["variables"])
	}
}

func TestServiceAssistantDetectedPidfile(t *testing.T) {
	// A candidate whose init definition yielded a pidfile path: it is prefilled
	// and written as `pidfile:` when accepted (blank keeps the default).
	env := Env{Daemons: func() ([]DaemonCandidate, error) {
		return []DaemonCandidate{{Name: "nginx", Title: "Nginx", Unit: "nginx", Pidfile: "/run/nginx.pid"}}, nil
	}}
	script := strings.Join([]string{"1", "", "1", ""}, "\n") + "\n" // select; pidfile=default; monitor enabled; interval inherit
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	svc := res.Services["nginx"].(map[string]any)
	if svc["pidfile"] != "/run/nginx.pid" {
		t.Fatalf("pidfile = %v, want /run/nginx.pid", svc["pidfile"])
	}
}

func TestServiceAssistantCommandMatchFallback(t *testing.T) {
	// No pidfile, but an exe was detected: accepting the fallback writes a
	// command_match process selector.
	env := Env{Daemons: func() ([]DaemonCandidate, error) {
		return []DaemonCandidate{{Name: "sshd", Title: "OpenSSH", Unit: "sshd", Exe: "/usr/sbin/sshd"}}, nil
	}}
	script := strings.Join([]string{"1", "", "y", "1", ""}, "\n") + "\n" // select; pidfile skip; match-by-exe yes; monitor enabled; interval inherit
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	procs := res.Services["sshd"].(map[string]any)["processes"].(map[string]any)
	main := procs["main"].(map[string]any)
	if main["type"] != "command_match" || main["exe"] != "/usr/sbin/sshd" {
		t.Fatalf("processes.main = %v, want command_match /usr/sbin/sshd", main)
	}
}

func TestServiceAssistantCommandPatternFallback(t *testing.T) {
	// A shared runtime/script service should use the detected cmdline pattern and
	// owner instead of assuming the configured command is the resolved exe.
	env := Env{Daemons: func() ([]DaemonCandidate, error) {
		return []DaemonCandidate{{Name: "homeassistant", Title: "Home Assistant", Unit: "homeassistant", Cmd: `(^|[[:space:]])/usr/bin/hass($|[[:space:]])`, User: "homeassistant"}}, nil
	}}
	script := strings.Join([]string{"1", "", "y", "1", ""}, "\n") + "\n" // select; pidfile skip; match-by-cmd yes; monitor enabled; interval inherit
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	procs := res.Services["homeassistant"].(map[string]any)["processes"].(map[string]any)
	main := procs["main"].(map[string]any)
	if main["type"] != "command_match" || main["cmd"] != `(^|[[:space:]])/usr/bin/hass($|[[:space:]])` || main["user"] != "homeassistant" {
		t.Fatalf("processes.main = %v, want command_match cmd+user", main)
	}
}

func TestServiceAssistantBatchMonitoring(t *testing.T) {
	// Selecting two services and answering "apply to all" asks monitor+interval
	// once and applies them to every selected service.
	env := Env{Daemons: func() ([]DaemonCandidate, error) {
		return []DaemonCandidate{{Name: "nginx", Unit: "nginx"}, {Name: "sshd", Unit: "sshd"}}, nil
	}}
	// select 1,2; per-service pidfile skips; batch=yes; monitor disabled; interval 30s.
	script := strings.Join([]string{"1,2", "", "", "y", "2", "30s"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{"nginx", "sshd"} {
		svc := res.Services[name].(map[string]any)
		if svc["monitor"] != "disabled" || svc["interval"] != "30s" {
			t.Fatalf("%s monitor/interval = %v / %v, want disabled / 30s", name, svc["monitor"], svc["interval"])
		}
	}
}

func TestServiceLabel(t *testing.T) {
	got := serviceLabel(DaemonCandidate{Title: "Nginx", Unit: "nginx", Port: 80, PortListening: true, UnitPresent: true, ConfigPaths: []string{"/etc/nginx/nginx.conf"}})
	for _, want := range []string{"Nginx", "unit: nginx", "port 80 (listening)", "config: /etc/nginx/nginx.conf"} {
		if !strings.Contains(got, want) {
			t.Fatalf("label %q missing %q", got, want)
		}
	}
}
