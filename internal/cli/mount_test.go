package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/mountctl"
)

func writeMountConfig(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	global := filepath.Join(root, "sermo.yml")
	mustWrite(t, global, `
paths:
  includes: [ `+root+`/enabled ]
  mounts: [ `+root+`/mounts ]
  runtime: `+root+`/run
defaults:
  policy:
    cooldown: 5m
`)
	mustWrite(t, filepath.Join(root, "mounts", "backup.yml"), `
kind: mount
name: mount-backup
path: /mnt/backup
`)
	return global
}

func mountApp(t *testing.T, mounted *bool, out *bytes.Buffer) App {
	t.Helper()
	return App{
		LoadConfig: config.Load,
		Env:        func(string) string { return "" },
		Stdout:     out,
		Stderr:     &bytes.Buffer{},
		MountController: func(cfg *config.Config) mountctl.Controller {
			runner := &fakeMountRunner{mounted: mounted}
			return mountctl.Controller{
				Runtime: cfg.Global.RuntimeDir(),
				Runner:  runner,
				Mounts: func() ([]checks.Mount, error) {
					if *mounted {
						return []checks.Mount{{MountPoint: "/mnt/backup"}}, nil
					}
					return nil, nil
				},
				InFstab: func(path string) (bool, error) { return path == "/mnt/backup", nil },
			}
		},
	}
}

type fakeMountRunner struct{ mounted *bool }

func (r *fakeMountRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	switch name {
	case "mount":
		if len(args) == 1 && args[0] == "/mnt/backup" {
			*r.mounted = true
		}
	case "umount":
		if len(args) == 1 && args[0] == "/mnt/backup" {
			*r.mounted = false
		}
	}
	return execx.Result{}, nil
}

func TestMountCommandByName(t *testing.T) {
	global := writeMountConfig(t)
	mounted := false
	var out bytes.Buffer
	app := mountApp(t, &mounted, &out)

	code := app.Run(context.Background(), []string{"--config", global, "mount", "mount-backup"})
	if code != exitSuccess {
		t.Fatalf("Run exit = %d, want success", code)
	}
	if !strings.Contains(out.String(), "mount-backup: mounted") {
		t.Fatalf("stdout = %q", out.String())
	}
}

func TestMountStatusByPathUsesConfiguredMount(t *testing.T) {
	global := writeMountConfig(t)
	mounted := true
	var out bytes.Buffer
	app := mountApp(t, &mounted, &out)

	code := app.Run(context.Background(), []string{"--config", global, "mount", "status", "/mnt/backup"})
	if code != exitSuccess {
		t.Fatalf("Run exit = %d, want success", code)
	}
	if got := out.String(); !strings.Contains(got, "name: mount-backup") || !strings.Contains(got, "mounted: true") {
		t.Fatalf("stdout = %q", got)
	}
}
