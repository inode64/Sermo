package config

import (
	"strings"
	"testing"
)

const mountGlobal = `
engine:
  backend: auto
paths:
  catalog: [ @ROOT@/catalog ]
  includes: [ @ROOT@/enabled ]
  mounts: [ @ROOT@/mounts ]
  runtime: /run/sermo
defaults:
  policy:
    cooldown: 5m
`

func TestLoadMountDocumentsFromMountsPath(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"mounts/backup.yml": `
kind: mount
name: mount-backup
display_name: Backup mount
category: storage
path: /mnt/backup
refcount: true
umount: { term_timeout: 12s, kill_timeout: 5s }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Mounts["mount-backup"]; !ok {
		t.Fatalf("mount-backup not loaded: %v", cfg.MountNames)
	}
	if got := cfg.MountNameByPath("/mnt/backup"); got != "mount-backup" {
		t.Fatalf("MountNameByPath = %q, want mount-backup", got)
	}
	if issues := Validate(cfg); len(issues) != 0 {
		t.Fatalf("Validate issues: %v", issues)
	}
}

func TestMountValidationRejectsUnsafeSIGKILLWithoutSelector(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"mounts/backup.yml": `
kind: mount
name: mount-backup
path: /mnt/backup
umount: { allow_sigkill: true }
`,
	})
	cfg, err := Load(global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, "umount.allow_sigkill=true requires stop_policy.kill_only_if") {
		t.Fatalf("Validate issues = %v, want allow_sigkill/kill_only_if error", issues)
	}
}

func TestMountDirOnlyAllowsMountDocuments(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"mounts/web.yml": `
kind: service
name: web
`,
	})
	_, err := Load(global)
	if err == nil || !strings.Contains(err.Error(), "mount config directories only support kind: mount") {
		t.Fatalf("Load error = %v, want mount-only directory error", err)
	}
}
