package assist

import (
	"strings"
	"testing"
)

func TestServiceAssistant(t *testing.T) {
	env := Env{
		Backend:      "openrc",
		ServiceNames: map[string]struct{}{"redis": {}}, // redis already configured -> skipped if chosen
		Daemons: func() ([]DaemonCandidate, error) {
			return []DaemonCandidate{
				{Name: "nginx", Title: "Nginx", Unit: "nginx", Port: 80, UnitPresent: true, ConfigPaths: []string{"/etc/nginx/nginx.conf"}},
				{Name: "named", Title: "BIND", Unit: "named", Port: 53, UnitPresent: true, PortListening: true},
			}, nil
		},
	}
	// Select nginx (1) with a port override, keep default name; then no more.
	script := strings.Join([]string{
		"1",    // MultiChoose -> nginx
		"",     // name: default nginx
		"8080", // port override
		"n",    // add another? no
	}, "\n") + "\n"

	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
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
	vars, _ := svc["variables"].(map[string]any)
	if vars == nil || vars["port"] != 8080 {
		t.Fatalf("expected port override 8080, got %v", svc["variables"])
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
