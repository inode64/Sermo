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
	if !strings.Contains(out.String(), "storage-mnt-backup") || !strings.Contains(out.String(), "free_pct") {
		t.Fatalf("generated YAML not shown: %s", out.String())
	}
	// The global config only points paths.includes at the wizard directory; the
	// watch itself is written as a separate enabled fragment.
	merged, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(merged), "storage-mnt-backup") {
		t.Fatalf("watch should not be in global config: %s", merged)
	}
	if !strings.Contains(string(merged), "includes:") || !strings.Contains(string(merged), "volume") {
		t.Fatalf("paths.includes not updated: %s", merged)
	}
	if !strings.Contains(string(merged), "interval: 30s") {
		t.Fatalf("merge dropped existing config: %s", merged)
	}
	watchPath := filepath.Join(tmp, "volume", "storage-mnt-backup.yml")
	watchFile, err := os.ReadFile(watchPath)
	if err != nil {
		t.Fatalf("watch file not written: %v", err)
	}
	if !strings.Contains(string(watchFile), "watches:") || !strings.Contains(string(watchFile), "storage-mnt-backup") || !strings.Contains(string(watchFile), "free_pct") {
		t.Fatalf("watch fragment wrong: %s", watchFile)
	}
	loaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load merged config: %v", err)
	}
	watches, _ := loaded.Global.Raw["watches"].(map[string]any)
	if _, ok := watches["storage-mnt-backup"]; !ok {
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
	if err := os.WriteFile(cfgPath, []byte("watches:\n  storage-root: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureNoWatchCollisions(cfg, map[string]any{"storage-root": map[string]any{}}); err == nil {
		t.Fatal("merging a watch that already exists must error")
	}
}

func TestMergeWizardWatchesRejectsExistingFile(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("paths:\n  includes: [volume]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(tmp, "volume"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "volume", "storage-root.yml"), []byte("watches: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := mergeWizardWatches(cfgPath, "volume", map[string]any{"storage-root": map[string]any{}}); err == nil {
		t.Fatal("existing watch file must not be overwritten")
	}
}

func TestMergeWizardWatchesMigratesLegacyEnabledPath(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "sermo.yml")
	if err := os.WriteFile(cfgPath, []byte("paths:\n  enabled: [apps-enabled]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	merged, err := mergeWizardWatches(cfgPath, "volume", map[string]any{"storage-root": map[string]any{"then": map[string]any{"notify": []any{"ops"}}}})
	if err != nil {
		t.Fatalf("mergeWizardWatches: %v", err)
	}
	if merged.Backup == "" {
		t.Fatal("legacy enabled path migration should rewrite global config")
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "enabled:") || !strings.Contains(string(data), "includes:") || !strings.Contains(string(data), "apps-enabled") || !strings.Contains(string(data), "volume") {
		t.Fatalf("legacy path not migrated to includes: %s", data)
	}
}
