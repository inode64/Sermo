package assist

import (
	"strings"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func serviceTestEnv() Env {
	return Env{
		Backend:      string(servicemgr.BackendOpenRC),
		ServiceNames: map[string]struct{}{"redis": {}}, // redis already configured -> skipped if chosen
		CatalogServices: func() ([]ServiceCandidate, error) {
			return []ServiceCandidate{
				{Name: "nginx", Title: "Nginx", Unit: "nginx", Status: string(servicemgr.StatusActive), Port: 80, UnitPresent: true, ConfigPaths: []string{"/etc/nginx/nginx.conf"}},
				{Name: "named", Title: "BIND", Unit: "named", Status: string(servicemgr.StatusActive), Port: 53, UnitPresent: true, PortListening: true},
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
		"y",    // add detected configuration watch
		"1",    // monitor state: enabled
		"",     // interval: inherit
		"y",    // dry-run automatic actions
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
	if svc[config.ServiceKeyUses] != "nginx" || svc[config.EntryKeyEnabled] != true {
		t.Fatalf("body = %v", svc)
	}
	if svc[config.EntryKeyMonitor] != config.MonitorEnabled {
		t.Fatalf("monitor = %v, want enabled", svc[config.EntryKeyMonitor])
	}
	if svc[config.EntryKeyDryRun] != true {
		t.Fatalf("dry_run = %v, want true", svc[config.EntryKeyDryRun])
	}
	vars, _ := svc[config.SectionVariables].(map[string]any)
	if vars == nil || vars[config.VariableKeyPort] != 8080 {
		t.Fatalf("expected port override 8080, got %v", svc[config.SectionVariables])
	}
	watches := svc[config.SectionWatches].(map[string]any)
	configWatch := watches[serviceConfigWatchName].(map[string]any)
	if configWatch[config.EntryKeyInterval] != serviceConfigWatchInterval {
		t.Fatalf("config watch interval = %v, want %s", configWatch[config.EntryKeyInterval], serviceConfigWatchInterval)
	}
	configCheck := configWatch[config.WatchKeyCheck].(map[string]any)
	if configCheck[checks.CheckKeyType] != checks.CheckTypeConfig || configCheck[checks.CheckKeyOnChange] != true {
		t.Fatalf("config check = %v, want config/on_change", configCheck)
	}
	paths := configCheck[checks.CheckKeyPath].([]any)
	if len(paths) != 1 || paths[0] != "/etc/nginx/nginx.conf" {
		t.Fatalf("config check paths = %v, want nginx.conf", paths)
	}
}

func TestServiceAssistantCatalogThenGenericServices(t *testing.T) {
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{
			{Name: "nginx", Title: "Nginx", Unit: "nginx", Status: string(servicemgr.StatusActive), Port: 80},
			{Name: "redis", Title: "Redis", Unit: "redis", Status: string(servicemgr.StatusInactive)},
			{Name: "customd", Title: "customd", Unit: "customd", Status: string(servicemgr.StatusActive), Generic: true, Pidfile: "/run/customd.pid"},
		}, nil
	}}
	script := strings.Join([]string{
		"1", // choose nginx from the active catalog list; redis is inactive
		"",  // keep catalog port
		"1", // monitor nginx
		"",  // interval inherit
		"n", // dry-run automatic actions
		"y", // review active units without catalog profiles
		"1", // choose customd
		"",  // accept detected pidfile
		"1", // monitor customd
		"",  // interval inherit
		"n", // dry-run automatic actions
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
	if nginx[config.ServiceKeyUses] != "nginx" {
		t.Fatalf("nginx uses = %v, want nginx", nginx[config.ServiceKeyUses])
	}
	if _, ok := nginx[config.ServiceKeyPidfile]; ok {
		t.Fatalf("catalog service must inherit pidfile from catalog: %v", nginx)
	}
	if _, ok := nginx[config.SectionProcesses]; ok {
		t.Fatalf("catalog service must inherit processes from catalog: %v", nginx)
	}
	custom := res.Services["customd"].(map[string]any)
	if _, ok := custom[config.ServiceKeyUses]; ok {
		t.Fatalf("generic service must not use catalog profile: %v", custom)
	}
	if custom[config.ServiceKeyService] != "customd" {
		t.Fatalf("generic service = %v, want customd", custom[config.ServiceKeyService])
	}
	watches := custom[config.SectionWatches].(map[string]any)
	serviceCheck := watches[serviceStatusWatchName].(map[string]any)[config.WatchKeyCheck].(map[string]any)
	if serviceCheck[checks.CheckKeyType] != checks.CheckTypeService || serviceCheck[checks.CheckKeyExpect] != string(servicemgr.StatusActive) {
		t.Fatalf("generic service check = %v, want service/active", serviceCheck)
	}
	if custom[config.ServiceKeyPidfile] != "/run/customd.pid" {
		t.Fatalf("generic pidfile = %v, want /run/customd.pid", custom[config.ServiceKeyPidfile])
	}
}

func TestServiceAssistantSkipsMissingStatus(t *testing.T) {
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{
			{Name: "nginx", Title: "Nginx", Unit: "nginx", Status: string(servicemgr.StatusActive)},
			{Name: "redis", Title: "Redis", Unit: "redis"},
		}, nil
	}}
	script := strings.Join([]string{config.SelectionKeywordAll, "1", "", "n"}, "\n") + "\n"
	var out strings.Builder
	p := NewPrompt(strings.NewReader(script), &out)
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out.String(), "Redis") {
		t.Fatalf("missing-status service was offered:\n%s", out.String())
	}
	if _, ok := res.Services["redis"]; ok {
		t.Fatalf("missing-status service was configured: %v", res.Services["redis"])
	}
}

func TestServiceAssistantCatalogDetectedPidfileIsInherited(t *testing.T) {
	// Catalog service profiles own PID detection. A detected pidfile must not be
	// written into the generated service override.
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{{Name: "nginx", Title: "Nginx", Unit: "nginx", Status: string(servicemgr.StatusActive), Pidfile: "/run/nginx.pid"}}, nil
	}}
	script := strings.Join([]string{"1", "1", "", "n"}, "\n") + "\n" // select; monitor enabled; interval inherit; no dry-run
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	svc := res.Services["nginx"].(map[string]any)
	if _, ok := svc[config.ServiceKeyPidfile]; ok {
		t.Fatalf("catalog service must not write pidfile override: %v", svc)
	}
	if _, ok := svc[config.SectionProcesses]; ok {
		t.Fatalf("catalog service must not write processes override: %v", svc)
	}
}

func TestServiceAssistantCatalogDetectedVariablesAreWritten(t *testing.T) {
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{{
			Name:      "ceph-mon",
			Title:     "Ceph Monitor",
			Unit:      "ceph-mon@node1.service",
			Status:    string(servicemgr.StatusActive),
			Port:      3300,
			Variables: map[string]any{"host": "192.0.2.102", "port": 3300},
		}}, nil
	}}
	script := strings.Join([]string{"1", "", "1", "", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	svc := res.Services["ceph-mon"].(map[string]any)
	vars := svc[config.SectionVariables].(map[string]any)
	if vars[config.VariableKeyHost] != "192.0.2.102" || vars[config.VariableKeyPort] != 3300 {
		t.Fatalf("variables = %v, want detected ceph endpoint", vars)
	}
}

func TestServiceAssistantGenericDetectedPidfile(t *testing.T) {
	// Generic services have no catalog service profile, so accepting the detected
	// pidfile writes it into the generated service entry.
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{{Name: "customd", Title: "customd", Unit: "customd", Status: string(servicemgr.StatusActive), Generic: true, Pidfile: "/run/customd.pid"}}, nil
	}}
	script := strings.Join([]string{"y", "1", "", "1", "", "n"}, "\n") + "\n" // review generic; select; pidfile=default; monitor enabled; interval inherit; no dry-run
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	svc := res.Services["customd"].(map[string]any)
	if svc[config.ServiceKeyPidfile] != "/run/customd.pid" {
		t.Fatalf("pidfile = %v, want /run/customd.pid", svc[config.ServiceKeyPidfile])
	}
}

func TestServiceAssistantSkipsGenericServicesWithNone(t *testing.T) {
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{{Name: "customd", Title: "customd", Unit: "customd", Status: string(servicemgr.StatusActive), Generic: true, Pidfile: "/run/customd.pid"}}, nil
	}}
	script := strings.Join([]string{"y", config.SelectionKeywordNone}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Services) != 0 {
		t.Fatalf("services = %v, want none", res.Services)
	}
}

func TestServiceAssistantRejectsNonAbsolutePidfile(t *testing.T) {
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{{Name: "customd", Title: "customd", Unit: "customd", Status: string(servicemgr.StatusActive), Generic: true, Pidfile: "/run/customd.pid"}}, nil
	}}
	script := strings.Join([]string{"y", "1", "y", "", "1", "", "n"}, "\n") + "\n" // review generic; invalid pidfile; accept default; monitor enabled; inherit interval; no dry-run
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
	if svc[config.ServiceKeyPidfile] != "/run/customd.pid" {
		t.Fatalf("pidfile = %v, want /run/customd.pid", svc[config.ServiceKeyPidfile])
	}
}

func TestServiceAssistantCommandMatchFallback(t *testing.T) {
	// No pidfile, but an exe was detected: accepting the fallback writes a
	// process selector.
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{{Name: "sshd", Title: "OpenSSH", Unit: "sshd", Status: string(servicemgr.StatusActive), Generic: true, Exe: "/usr/sbin/sshd"}}, nil
	}}
	script := strings.Join([]string{"y", "1", "", "y", "1", "", "n"}, "\n") + "\n" // review generic; select; pidfile skip; match-by-exe yes; monitor enabled; interval inherit; no dry-run
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	procs := res.Services["sshd"].(map[string]any)[config.SectionProcesses].(map[string]any)
	main := procs[process.RoleMain].(map[string]any)
	if _, present := main[config.EntryKeyType]; present || main[process.SelectorKeyExe] != "/usr/sbin/sshd" {
		t.Fatalf("processes.main = %v, want /usr/sbin/sshd without type", main)
	}
}

func TestServiceAssistantCommandPatternFallback(t *testing.T) {
	// A shared runtime/script service should use the detected cmdline pattern and
	// owner instead of assuming the configured command is the resolved exe.
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{{Name: "homeassistant", Title: "Home Assistant", Unit: "homeassistant", Status: string(servicemgr.StatusActive), Generic: true, Cmd: `(^|[[:space:]])/usr/bin/hass($|[[:space:]])`, User: "homeassistant"}}, nil
	}}
	script := strings.Join([]string{"y", "1", "", "y", "1", "", "n"}, "\n") + "\n" // review generic; select; pidfile skip; match-by-cmd yes; monitor enabled; interval inherit; no dry-run
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	procs := res.Services["homeassistant"].(map[string]any)[config.SectionProcesses].(map[string]any)
	main := procs[process.RoleMain].(map[string]any)
	if _, present := main[config.EntryKeyType]; present || main[process.SelectorKeyCmd] != `(^|[[:space:]])/usr/bin/hass($|[[:space:]])` || main[process.SelectorKeyUser] != "homeassistant" {
		t.Fatalf("processes.main = %v, want cmd+user without type", main)
	}
}

func TestServiceAssistantBatchMonitoring(t *testing.T) {
	// Selecting two services and answering "apply to all" asks monitor+interval
	// once and applies them to every selected service.
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{{Name: "nginx", Unit: "nginx", Status: string(servicemgr.StatusActive)}, {Name: "sshd", Unit: "sshd", Status: string(servicemgr.StatusActive)}}, nil
	}}
	// select 1,2; batch=yes; monitor disabled; interval 30s; dry-run=no.
	script := strings.Join([]string{"1,2", "y", "2", "30s", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{"nginx", "sshd"} {
		svc := res.Services[name].(map[string]any)
		if svc[config.EntryKeyMonitor] != config.MonitorDisabled || svc[config.EntryKeyInterval] != "30s" {
			t.Fatalf("%s monitor/interval = %v / %v, want disabled / 30s", name, svc[config.EntryKeyMonitor], svc[config.EntryKeyInterval])
		}
	}
}

func TestServiceAssistantBatchSkipsPortPromptsByDefault(t *testing.T) {
	env := Env{CatalogServices: func() ([]ServiceCandidate, error) {
		return []ServiceCandidate{
			{Name: "apache", Unit: "apache2", Status: string(servicemgr.StatusActive), Port: 80},
			{Name: "redis", Unit: "redis", Status: string(servicemgr.StatusActive), Port: 6379},
		}, nil
	}}
	// select all; do not review port overrides; batch=yes; monitor enabled;
	// inherit interval; dry-run=yes. The script deliberately has no blank lines
	// for individual port prompts.
	script := strings.Join([]string{config.SelectionKeywordAll, "n", "y", "1", "", "y"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := serviceAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, name := range []string{"apache", "redis"} {
		svc := res.Services[name].(map[string]any)
		if _, hasVars := svc[config.SectionVariables]; hasVars {
			t.Fatalf("%s should not have port override variables when review is skipped: %v", name, svc)
		}
		if svc[config.EntryKeyDryRun] != true {
			t.Fatalf("%s dry_run = %v, want true", name, svc[config.EntryKeyDryRun])
		}
	}
}

func TestServiceLabel(t *testing.T) {
	got := serviceLabel(ServiceCandidate{Title: "Nginx", Unit: "nginx", Port: 80, PortListening: true, Variables: map[string]any{"host": "192.0.2.22"}, UnitPresent: true, ConfigPaths: []string{"/etc/nginx/nginx.conf"}})
	for _, want := range []string{"Nginx", "unit: nginx", "port 80 (listening)", "host: 192.0.2.22", "config: /etc/nginx/nginx.conf"} {
		if !strings.Contains(got, want) {
			t.Fatalf("label %q missing %q", got, want)
		}
	}
}
