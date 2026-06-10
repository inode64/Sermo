package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/execx"
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
