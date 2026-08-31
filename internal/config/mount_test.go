package config

import (
	"testing"

	"sermo/internal/cfgval"
)

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

func TestResolveStoragesReturnsResolvedStorageWatchesInNameOrder(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal + `
  variables:
    mount_root: /mnt
`,
		"mounts/z-data.yml": `
name: z-data
check: { type: storage, path: "${mount_root}/data", mounted: true }
`,
		"mounts/a-backup.yml": `
name: a-backup
display_name: Backup mount
check: { type: storage, path: "${mount_root}/backup", mounted: true }
mount: {}
`,
		"mounts/load.yml": `
name: load
check: { type: load, load5: { op: ">", value: 3 } }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	storages, errs := cfg.ResolveStorages()
	if len(errs) != 0 {
		t.Fatalf("ResolveStorages() errors = %v", errs)
	}
	if len(storages) != 2 {
		t.Fatalf("ResolveStorages() = %+v, want two entries", storages)
	}
	if storages[0].Name != "a-backup" || storages[1].Name != "z-data" {
		t.Fatalf("ResolveStorages() names = %q, %q, want a-backup, z-data", storages[0].Name, storages[1].Name)
	}
	if got := cfgval.String(storages[0].Tree[EntryKeyPath]); got != "/mnt/backup" {
		t.Fatalf("a-backup path = %q, want /mnt/backup", got)
	}
	if _, ok := storages[0].Tree[StorageKeyMount].(map[string]any); !ok {
		t.Fatalf("a-backup tree = %+v, want resolved mount block", storages[0].Tree)
	}
}

func TestMountValidationRejectsRemovedUmountEscalationKeys(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"mounts/backup.yml": `
name: mount-backup
check: { type: storage, path: /mnt/backup, mounted: true }
mount:
  umount: { allow_sigkill: true, allow_lazy: true }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, `mount.umount key "allow_sigkill" is not one of term_timeout, kill_timeout`) ||
		!hasIssue(issues, `mount.umount key "allow_lazy" is not one of term_timeout, kill_timeout`) {
		t.Fatalf("Validate issues = %v, want removed umount escalation keys rejected", issues)
	}
}

func TestMountValidationRejectsStopPolicyActionPermission(t *testing.T) {
	global := writeConfig(t, map[string]string{
		"sermo.yml": mountGlobal,
		"mounts/backup.yml": `
name: mount-backup
check: { type: storage, path: /mnt/backup, mounted: true }
mount:
  stop_policy:
    force_kill: true
    kill_only_if: { users: [root], exe_any: [/usr/bin/rsync] }
`,
	})
	cfg, err := loadConfig(t, global)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	issues := Validate(cfg)
	if !hasIssue(issues, `mount.stop_policy key "force_kill" is not one of kill_only_if`) {
		t.Fatalf("Validate issues = %v, want mount stop_policy force_kill rejected", issues)
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
