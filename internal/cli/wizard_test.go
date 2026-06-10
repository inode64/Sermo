package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/assist"
	"sermo/internal/config"
)

func fakeWizardEnv(*config.Config) assist.Env {
	return assist.Env{
		Notifiers: []string{"ops-email"},
		Volumes: func() ([]assist.Volume, error) {
			return []assist.Volume{{Mountpoint: "/mnt/backup", FSType: "ext4", Device: "/dev/mapper/vg0-data"}}, nil
		},
		Ifaces: func() ([]assist.Iface, error) { return nil, nil },
	}
}

func TestRunWizardVolumeMergesConfig(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("engine:\n  interval: 30s\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// volume assistant: select vol 1; free<10; for 3; notifier ops-email; no expand.
	// then runWizard: confirm merge with "y".
	script := strings.Join([]string{"1", "1", "10", "3", "2", "n", "y"}, "\n") + "\n"

	var out bytes.Buffer
	app := App{
		Stdin:         strings.NewReader(script),
		Stdout:        &out,
		Stderr:        &bytes.Buffer{},
		LoadConfig:    config.Load,
		wizardEnvFunc: fakeWizardEnv,
	}
	code := app.Run(context.Background(), []string{"--config", cfgPath, "wizard", "volume"})
	if code != exitSuccess {
		t.Fatalf("exit = %d, want success; out=%s", code, out.String())
	}

	// The generated block was printed.
	if !strings.Contains(out.String(), "disk-mnt-backup") || !strings.Contains(out.String(), "free_pct") {
		t.Fatalf("generated YAML not shown: %s", out.String())
	}
	// The config was merged and a backup written.
	merged, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(merged), "watches:") || !strings.Contains(string(merged), "disk-mnt-backup") {
		t.Fatalf("config not merged: %s", merged)
	}
	if !strings.Contains(string(merged), "interval: 30s") {
		t.Fatalf("merge dropped existing config: %s", merged)
	}
	bak, err := os.ReadFile(cfgPath + ".bak")
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if strings.Contains(string(bak), "watches:") {
		t.Fatalf("backup should be the original (pre-merge): %s", bak)
	}
}

func TestRunWizardUnknownAssistant(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	_ = os.WriteFile(cfgPath, []byte("engine: {}\n"), 0o644)
	app := App{
		Stdin:         strings.NewReader(""),
		Stdout:        &bytes.Buffer{},
		Stderr:        &bytes.Buffer{},
		LoadConfig:    config.Load,
		wizardEnvFunc: fakeWizardEnv,
	}
	if code := app.Run(context.Background(), []string{"--config", cfgPath, "wizard", "nope"}); code != exitUsage {
		t.Fatalf("unknown assistant exit = %d, want usage error", code)
	}
}

func TestMergeWatchesRejectsCollision(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("watches:\n  disk-root: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeWatches(cfgPath, map[string]any{"disk-root": map[string]any{}}); err == nil {
		t.Fatal("merging a watch that already exists must error")
	}
}
