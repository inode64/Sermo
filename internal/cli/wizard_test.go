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
	// The global config only points paths.enabled at the wizard directory; the
	// watch itself is written as a separate enabled fragment.
	merged, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(merged), "disk-mnt-backup") {
		t.Fatalf("watch should not be in global config: %s", merged)
	}
	if !strings.Contains(string(merged), "enabled:") || !strings.Contains(string(merged), "volume") {
		t.Fatalf("paths.enabled not updated: %s", merged)
	}
	if !strings.Contains(string(merged), "interval: 30s") {
		t.Fatalf("merge dropped existing config: %s", merged)
	}
	watchPath := filepath.Join(tmp, "volume", "disk-mnt-backup.yml")
	watchFile, err := os.ReadFile(watchPath)
	if err != nil {
		t.Fatalf("watch file not written: %v", err)
	}
	if !strings.Contains(string(watchFile), "watches:") || !strings.Contains(string(watchFile), "disk-mnt-backup") || !strings.Contains(string(watchFile), "free_pct") {
		t.Fatalf("watch fragment wrong: %s", watchFile)
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load merged config: %v", err)
	}
	watches, _ := loaded.Global.Raw["watches"].(map[string]any)
	if _, ok := watches["disk-mnt-backup"]; !ok {
		t.Fatalf("loaded config did not include generated watch: %v", watches)
	}
	bak, err := os.ReadFile(cfgPath + ".bak")
	if err != nil {
		t.Fatalf("backup not written: %v", err)
	}
	if strings.Contains(string(bak), "paths:") || strings.Contains(string(bak), "watches:") {
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

func TestWizardRejectsLoadedWatchCollision(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("watches:\n  disk-root: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureNoWatchCollisions(cfg, map[string]any{"disk-root": map[string]any{}}); err == nil {
		t.Fatal("merging a watch that already exists must error")
	}
}

func TestMergeWizardWatchesRejectsExistingFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("paths:\n  enabled: [volume]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, "volume"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "volume", "disk-root.yml"), []byte("watches: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeWizardWatches(cfgPath, "volume", map[string]any{"disk-root": map[string]any{}}); err == nil {
		t.Fatal("existing watch file must not be overwritten")
	}
}
