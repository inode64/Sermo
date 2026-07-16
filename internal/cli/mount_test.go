package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/mountctl"
	"sermo/internal/state"
)

func writeMountConfig(t *testing.T) string {
	t.Helper()
	global := writeServiceConfig(t, `
paths:
  services: [ @ROOT@/services ]
  watches: [ @ROOT@/mounts ]
  runtime: @ROOT@/run
  state: @ROOT@/state
defaults:
  policy:
    cooldown: 5m
`, map[string]string{
		"mounts/backup.yml": `
name: mount-backup
check:
  type: storage
  path: /mnt/backup
  mounted: true
mount: {}
`,
	})
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
	case mountctl.ActionMount:
		if len(args) == 1 && args[0] == "/mnt/backup" {
			*r.mounted = true
		}
	case mountctl.ActionUmount:
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

func TestMountControllerUsesMountDefaultTimeoutUnlessFlagSet(t *testing.T) {
	global := writeMountConfig(t)
	cfg, err := config.Load(global)
	if err != nil {
		t.Fatal(err)
	}
	app := App{Runner: execx.CommandRunner{}}

	ctrl := app.mountController(cfg, options{timeout: time.Hour})
	if ctrl.CommandTimeout != 0 {
		t.Fatalf("CommandTimeout without explicit flag = %s, want mountctl default", ctrl.CommandTimeout)
	}

	ctrl = app.mountController(cfg, options{timeout: 5 * time.Second, timeoutSet: true})
	if ctrl.CommandTimeout != 5*time.Second {
		t.Fatalf("CommandTimeout with explicit flag = %s, want 5s", ctrl.CommandTimeout)
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
	} else if strings.Contains(got, "source:") {
		t.Fatalf("stdout should not expose mount source: %q", got)
	}
}

func TestUmountRootIsRejected(t *testing.T) {
	global := writeMountConfig(t)
	mounted := true
	var out bytes.Buffer
	app := mountApp(t, &mounted, &out)

	code := app.Run(context.Background(), []string{"--config", global, "umount", "/"})
	if code != exitRuntimeError {
		t.Fatalf("umount / exit = %d, want runtime error", code)
	}
	if got := out.String(); !strings.Contains(got, "root filesystem cannot be unmounted") {
		t.Fatalf("stdout = %q, want root protection message", got)
	}
	if !mounted {
		t.Fatal("umount / changed mounted state")
	}
}

func TestUmountCommandPausesStorageWatch(t *testing.T) {
	global := writeMountConfig(t)
	root := filepath.Dir(global)
	mounted := true
	var out bytes.Buffer
	app := mountApp(t, &mounted, &out)

	code := app.Run(context.Background(), []string{"--config", global, "umount", "mount-backup"})
	if code != exitSuccess {
		t.Fatalf("umount exit = %d, want success", code)
	}
	store, err := state.OpenContext(context.Background(), filepath.Join(root, "state", state.Filename))
	if err != nil {
		t.Fatalf("open state: %v", err)
	}
	rec, found, err := store.MonitorState("watch:mount-backup")
	if err != nil || !found || rec.Active || rec.Source != state.SourceCLIMountUmount {
		t.Fatalf("state after umount = %+v found=%v err=%v", rec, found, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close state: %v", err)
	}

	out.Reset()
	code = app.Run(context.Background(), []string{"--config", global, "mount", "mount-backup"})
	if code != exitSuccess {
		t.Fatalf("mount exit = %d, want success", code)
	}
	store, err = state.OpenContext(context.Background(), filepath.Join(root, "state", state.Filename))
	if err != nil {
		t.Fatalf("reopen state: %v", err)
	}
	defer store.Close()
	rec, found, err = store.MonitorState("watch:mount-backup")
	if err != nil || !found || !rec.Active || rec.Source != state.SourceCLI {
		t.Fatalf("state after mount = %+v found=%v err=%v", rec, found, err)
	}
}
