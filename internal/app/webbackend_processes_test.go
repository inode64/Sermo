package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func writeWebProcessConfig(t *testing.T, pidfile string) *config.Config {
	t.Helper()
	root := t.TempDir()
	enabled := filepath.Join(root, "enabled")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(globalPath, []byte(`
paths:
  includes: [`+enabled+`]
defaults:
  policy:
    cooldown: 5m
`), 0o644); err != nil {
		t.Fatal(err)
	}
	svcPath := filepath.Join(enabled, "mysql-main.yml")
	if err := os.WriteFile(svcPath, []byte(`
kind: service
name: mysql-main
service: { name: mysql }
processes:
  pidfile:
    type: pidfile
    path: `+pidfile+`
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(globalPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	return cfg
}

func TestWebBackendDetailProcessesRealPidfile(t *testing.T) {
	dir := t.TempDir()
	pidfile := filepath.Join(dir, "self.pid")
	if err := os.WriteFile(pidfile, []byte(itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := writeWebProcessConfig(t, pidfile)

	wb, warnings := NewWebBackend(cfg, Deps{Backend: "systemd", Manager: fakeManager{}, ExecxRunner: execx.CommandRunner{}})
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}

	detail, ok := wb.Detail(context.Background(), "mysql-main")
	if !ok {
		t.Fatal("detail not found")
	}
	if len(detail.Processes) == 0 {
		t.Fatal("expected at least one process from pidfile")
	}
	found := false
	for _, p := range detail.Processes {
		if p.PID == os.Getpid() {
			found = true
			if p.Source != "pidfile" {
				t.Fatalf("self process source = %q, want pidfile", p.Source)
			}
			if p.Role != "pidfile" {
				t.Fatalf("self process role = %q, want pidfile", p.Role)
			}
		}
	}
	if !found {
		t.Fatalf("processes = %+v, want pid %d", detail.Processes, os.Getpid())
	}
}

func TestWebBackendDetailProcessesNone(t *testing.T) {
	cfg := writeWebProcessConfig(t, "/nonexistent/pidfile.pid")
	wb, _ := NewWebBackend(cfg, Deps{Backend: "systemd", Manager: fakeManager{}, ExecxRunner: execx.CommandRunner{}})

	detail, ok := wb.Detail(context.Background(), "mysql-main")
	if !ok {
		t.Fatal("detail not found")
	}
	if detail.Processes != nil {
		t.Fatalf("processes = %+v, want nil/empty", detail.Processes)
	}
	if len(detail.ProcessWarnings) != 1 {
		t.Fatalf("ProcessWarnings = %+v, want 1 warning", detail.ProcessWarnings)
	}
	if !strings.Contains(detail.ProcessWarnings[0], "/nonexistent/pidfile.pid") {
		t.Fatalf("ProcessWarnings[0] = %q, want pidfile path", detail.ProcessWarnings[0])
	}
}

func TestInitDerivedProcessSelectors(t *testing.T) {
	tests := []struct {
		name string
		info servicemgr.ProcInfo
		want process.Selector
	}{
		{
			name: "pidfile",
			info: servicemgr.ProcInfo{Pidfile: "/run/app.pid", Exe: "/usr/bin/app", User: "app"},
			want: process.Selector{Name: "init", Type: process.SelectorPidfile, Paths: []string{"/run/app.pid"}},
		},
		{
			name: "cmd with user",
			info: servicemgr.ProcInfo{Cmd: `(^|[[:space:]])/usr/bin/app($|[[:space:]])`, User: "app"},
			want: process.Selector{Name: "init", Type: process.SelectorCommandMatch, Cmd: `(^|[[:space:]])/usr/bin/app($|[[:space:]])`, User: "app"},
		},
		{
			name: "exe with user",
			info: servicemgr.ProcInfo{Exe: "/usr/bin/app", User: "app"},
			want: process.Selector{Name: "init", Type: process.SelectorCommandMatch, Exe: "/usr/bin/app", User: "app"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := initDerivedProcessSelectors(tc.info)
			if len(got) != 1 {
				t.Fatalf("selectors = %+v, want one", got)
			}
			if got[0].Name != tc.want.Name || got[0].Type != tc.want.Type || got[0].Exe != tc.want.Exe || got[0].Cmd != tc.want.Cmd || got[0].User != tc.want.User || strings.Join(got[0].Paths, ",") != strings.Join(tc.want.Paths, ",") {
				t.Fatalf("selector = %+v, want %+v", got[0], tc.want)
			}
		})
	}
}

func TestInitDerivedProcessSelectorsRequireUserForCommandMatch(t *testing.T) {
	for _, info := range []servicemgr.ProcInfo{
		{Exe: "/usr/bin/app"},
		{Cmd: `(^|[[:space:]])/usr/bin/app($|[[:space:]])`},
	} {
		if got := initDerivedProcessSelectors(info); len(got) != 0 {
			t.Fatalf("selectors = %+v, want none without user", got)
		}
	}
}

func TestWebBackendDetailIncludesProcessSelectorWarnings(t *testing.T) {
	root := t.TempDir()
	enabled := filepath.Join(root, "enabled")
	if err := os.MkdirAll(enabled, 0o755); err != nil {
		t.Fatal(err)
	}
	globalPath := filepath.Join(root, "sermo.yml")
	if err := os.WriteFile(globalPath, []byte(`
paths:
  includes: [`+enabled+`]
defaults:
  policy:
    cooldown: 5m
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(enabled, "web.yml"), []byte(`
kind: service
name: web
service: { name: web }
processes:
  main:
    type: command_match
`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(globalPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	wb, warnings := NewWebBackend(cfg, Deps{Backend: servicemgr.BackendOpenRC, Manager: fakeManager{}, ExecxRunner: execx.CommandRunner{}})
	if len(warnings) > 0 {
		t.Fatalf("NewWebBackend warnings: %v", warnings)
	}
	detail, ok := wb.Detail(context.Background(), "web")
	if !ok {
		t.Fatal("detail not found")
	}
	if len(detail.ProcessWarnings) != 1 || !strings.Contains(detail.ProcessWarnings[0], "requires exe or cmd") {
		t.Fatalf("ProcessWarnings = %+v, want command_match warning", detail.ProcessWarnings)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
