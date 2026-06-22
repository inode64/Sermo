package app

import (
	"context"
	"path/filepath"
	"testing"

	"sermo/internal/checks"
	"sermo/internal/config"
)

func TestWebBackendMounts(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "run")
	cfg := &config.Config{
		Global: config.Global{Runtime: runtime, Raw: map[string]any{
			"paths": map[string]any{"runtime": runtime},
		}},
		Mounts: map[string]*config.Document{
			"mount-backup": {
				Body: map[string]any{
					"kind": "mount",
					"name": "mount-backup",
					"path": "/mnt/backup",
				},
			},
		},
		MountNames: []string{"mount-backup"},
	}
	b, warns := NewWebBackend(cfg, Deps{
		MountSampler: func() ([]checks.Mount, error) {
			return []checks.Mount{{MountPoint: "/mnt/backup", Device: "/dev/sdb1"}}, nil
		},
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	mounts := b.Mounts(context.Background())
	if len(mounts) != 1 {
		t.Fatalf("mounts = %+v, want one entry", mounts)
	}
	if mounts[0].Name != "mount-backup" || mounts[0].Path != "/mnt/backup" || !mounts[0].Mounted {
		t.Fatalf("mount = %+v", mounts[0])
	}
	if mounts[0].State != "active" || mounts[0].Refcount != 0 {
		t.Fatalf("mount state/refcount = %+v", mounts[0])
	}
}
