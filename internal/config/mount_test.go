package config

import (
	"strings"
	"testing"
)

const mountGlobal = `
engine:
  backend: auto
paths:
  services: [ @ROOT@/services ]
  storages: [ @ROOT@/storages ]
  runtime: /run/sermo
defaults:
  policy:
    cooldown: 5m
`

func TestLoadMountDocumentsFromStoragesPath(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"storages/backup.yml": `
name: mount-backup
display_name: Backup mount
category: storage
path: /mnt/backup
mount:
  refcount: true
  umount: { term_timeout: 12s, kill_timeout: 5s }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Storages["mount-backup"]; !ok {
		t.Fatalf("mount-backup not loaded: %v", cfg.StorageNames)
	}
	if got := cfg.StorageNameByPath("/mnt/backup"); got != "mount-backup" {
		t.Fatalf("StorageNameByPath = %q, want mount-backup", got)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate issues: %v", issues)
	}
}

func TestMountValidationRejectsUnsafeSIGKILLWithoutSelector(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"storages/backup.yml": `
name: mount-backup
path: /mnt/backup
mount:
  umount: { allow_sigkill: true }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, "mount.umount.allow_sigkill=true requires mount.stop_policy.kill_only_if") {
		t.Fatalf("Validate issues = %v, want allow_sigkill/kill_only_if error", issues)
	}
}

func TestStorageDirOnlyAllowsStorageDocuments(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"storages/web.yml": `
kind: service
name: web
`,
	})
	_, err := loadConfig(t, global)
	if err == nil || !strings.Contains(err.Error(), "located under a storage directory but declares kind: service") {
		t.Fatalf("Load error = %v, want storage-only directory error", err)
	}
}
