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
	// Select nginx (1) with a port override; monitor enabled, inherit interval.
	// The wizard never asks for a name; it is the candidate's.
	script := strings.Join([]string{
		"1",    // MultiChoose -> nginx
		"8080", // port override
		"1",    // monitor state: enabled
		"",     // interval: inherit
		"y",    // remediation shadow mode
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
	remediation := svc["remediation"].(map[string]any)
	if remediation["shadow"] != true {
		t.Fatalf("remediation = %v, want shadow true", remediation)
	}
	vars, _ := svc["variables"].(map[string]any)
	if vars == nil || vars["port"] != 8080 {
		t.Fatalf("expected port override 8080, got %v", svc["variables"])
	}
}

func TestServiceAssistantCatalogThenGenericServices(t *testing.T) {
	env := Env{Daemons: func() ([]DaemonCandidate, error) {
		return []DaemonCandidate{
			{Name: "nginx", Title: "Nginx", Unit: "nginx", Status: "active", Port: 80},
			{Name: "redis", Title: "Redis", Unit: "redis", Status: "inactive"},
			{Name: "customd", Title: "customd", Unit: "customd", Status: "active", Generic: true, Pidfile: "/run/customd.pid"},
		}, nil
	}}
	script := strings.Join([]string{
		"1", // choose nginx from the active catalog list; redis is inactive
		"",  // keep catalog port
		"1", // monitor nginx
		"",  // interval inherit
		"n", // remediation shadow
		"y", // review active units without catalog profiles
		"1", // choose customd
		"",  // accept detected pidfile
		"1", // monitor customd
		"",  // interval inherit
		"n", // remediation shadow
	}, "\n") + "\n"
	var out strings.Builder
	p := NewPrompt(strings.NewReader(script), &out)
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out.String(), "Redis") {
		t.Fatalf("inactive catalog service was offered:\n%s", out.String())
	}
	nginx := res.Services["nginx"].(map[string]any)
	if nginx["uses"] != "nginx" {
		t.Fatalf("nginx uses = %v, want nginx", nginx["uses"])
	}
	if _, ok := nginx["pidfile"]; ok {
		t.Fatalf("catalog service must inherit pidfile from catalog: %v", nginx)
	}
	if _, ok := nginx["processes"]; ok {
		t.Fatalf("catalog service must inherit processes from catalog: %v", nginx)
	}
	custom := res.Services["customd"].(map[string]any)
	if _, ok := custom["uses"]; ok {
		t.Fatalf("generic service must not use catalog profile: %v", custom)
	}
	service := custom["service"].(map[string]any)
	if service["name"] != "customd" {
		t.Fatalf("generic service.name = %v, want customd", service["name"])
	}
	checks := custom["checks"].(map[string]any)
	serviceCheck := checks["service"].(map[string]any)
	if serviceCheck["type"] != "service" || serviceCheck["expect"] != "active" {
		t.Fatalf("generic service check = %v, want service/active", serviceCheck)
	}
	if custom["pidfile"] != "/run/customd.pid" {
		t.Fatalf("generic pidfile = %v, want /run/customd.pid", custom["pidfile"])
	}
}

func TestServiceAssistantCatalogDetectedPidfileIsInherited(t *testing.T) {
	// Catalog daemon profiles own PID detection. A detected pidfile must not be
	// written into the generated service override.
	env := Env{Daemons: func() ([]DaemonCandidate, error) {
		return []DaemonCandidate{{Name: "nginx", Title: "Nginx", Unit: "nginx", Pidfile: "/run/nginx.pid"}}, nil
	}}
	script := strings.Join([]string{"1", "1", "", "n"}, "\n") + "\n" // select; monitor enabled; interval inherit; no shadow
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	svc := res.Services["nginx"].(map[string]any)
	if _, ok := svc["pidfile"]; ok {
		t.Fatalf("catalog service must not write pidfile override: %v", svc)
	}
	if _, ok := svc["processes"]; ok {
		t.Fatalf("catalog service must not write processes override: %v", svc)
	}
}

func TestServiceAssistantGenericDetectedPidfile(t *testing.T) {
	// Generic services have no catalog daemon profile, so accepting the detected
	// pidfile writes it into the generated service entry.
	env := Env{Daemons: func() ([]DaemonCandidate, error) {
		return []DaemonCandidate{{Name: "customd", Title: "customd", Unit: "customd", Generic: true, Pidfile: "/run/customd.pid"}}, nil
	}}
	script := strings.Join([]string{"y", "1", "", "1", "", "n"}, "\n") + "\n" // review generic; select; pidfile=default; monitor enabled; interval inherit; no shadow
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	svc := res.Services["customd"].(map[string]any)
	if svc["pidfile"] != "/run/customd.pid" {
		t.Fatalf("pidfile = %v, want /run/customd.pid", svc["pidfile"])
	}
}

func TestServiceAssistantRejectsNonAbsolutePidfile(t *testing.T) {
	env := Env{Daemons: func() ([]DaemonCandidate, error) {
		return []DaemonCandidate{{Name: "customd", Title: "customd", Unit: "customd", Generic: true, Pidfile: "/run/customd.pid"}}, nil
	}}
	script := strings.Join([]string{"y", "1", "y", "", "1", "", "n"}, "\n") + "\n" // review generic; invalid pidfile; accept default; monitor enabled; inherit interval; no shadow
	var out strings.Builder
	p := NewPrompt(strings.NewReader(script), &out)
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(out.String(), "pidfile must be an absolute path or blank") {
		t.Fatalf("expected validation message, got:\n%s", out.String())
	}
	svc := res.Services["customd"].(map[string]any)
	if svc["pidfile"] != "/run/customd.pid" {
		t.Fatalf("pidfile = %v, want /run/customd.pid", svc["pidfile"])
	}
}

func TestServiceAssistantCommandMatchFallback(t *testing.T) {
	// No pidfile, but an exe was detected: accepting the fallback writes a
	// command_match process selector.
	env := Env{Daemons: func() ([]DaemonCandidate, error) {
		return []DaemonCandidate{{Name: "sshd", Title: "OpenSSH", Unit: "sshd", Generic: true, Exe: "/usr/sbin/sshd"}}, nil
	}}
	script := strings.Join([]string{"y", "1", "", "y", "1", "", "n"}, "\n") + "\n" // review generic; select; pidfile skip; match-by-exe yes; monitor enabled; interval inherit; no shadow
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
		return []DaemonCandidate{{Name: "homeassistant", Title: "Home Assistant", Unit: "homeassistant", Generic: true, Cmd: `(^|[[:space:]])/usr/bin/hass($|[[:space:]])`, User: "homeassistant"}}, nil
	}}
	script := strings.Join([]string{"y", "1", "", "y", "1", "", "n"}, "\n") + "\n" // review generic; select; pidfile skip; match-by-cmd yes; monitor enabled; interval inherit; no shadow
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
	// select 1,2; batch=yes; monitor disabled; interval 30s; shadow=no.
	script := strings.Join([]string{"1,2", "y", "2", "30s", "n"}, "\n") + "\n"
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
