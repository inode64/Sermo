package config

import "testing"

const mountGlobal = `
engine:
  backend: auto
paths:
  services: [ @ROOT@/services ]
  watches: [ @ROOT@/mounts ]
  runtime: /run/sermo
defaults:
  policy:
    cooldown: 5m
`

func TestLoadMountWatchFromWatchesPath(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"mounts/backup.yml": `
name: mount-backup
display_name: Backup mount
category: storage
check: { type: storage, path: /mnt/backup, mounted: true }
mount:
  refcount: true
  umount: { term_timeout: 12s, kill_timeout: 5s }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := cfg.StorageMountNames(); len(got) != 1 || got[0] != "mount-backup" {
		t.Fatalf("mount-backup not loaded: %v", got)
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
		"mounts/backup.yml": `
name: mount-backup
check: { type: storage, path: /mnt/backup, mounted: true }
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

func TestMountBlockRequiresStorageWatch(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"mounts/web.yml": `
name: web
check: { type: load, load5: { op: ">", value: 3 } }
mount: {}
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if issues := Validate(cfg); !hasIssue(issues, "watches.web.mount is only valid on a storage watch") {
		t.Fatalf("Validate issues = %v, want mount/storage-watch error", issues)
	}
}
