package assist

import (
	"strings"
	"testing"
)

func TestMountAssistantGeneratesMountUnit(t *testing.T) {
	env := Env{
		Mounts: func() ([]MountCandidate, error) {
			return []MountCandidate{{
				Path:    "/mnt/backup",
				Source:  "UUID=backup",
				FSType:  "ext4",
				Mounted: true,
			}}, nil
		},
	}
	p := NewPrompt(strings.NewReader("1\ny\n"), &strings.Builder{})
	res, err := (mountAssistant{}).Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	body, ok := res.Mounts["mount-mnt-backup"].(map[string]any)
	if !ok {
		t.Fatalf("mount-mnt-backup missing: %+v", res.Mounts)
	}
	mount, ok := body["mount"].(map[string]any)
	if !ok {
		t.Fatalf("mount block missing: %+v", body)
	}
	if body["path"] != "/mnt/backup" || mount["refcount"] != true {
		t.Fatalf("mount body = %+v, want path/refcount", body)
	}
	umount, ok := mount["umount"].(map[string]any)
	if !ok || umount["allow_sigkill"] != false || umount["allow_lazy"] != false {
		t.Fatalf("umount policy = %+v, want safe defaults", mount["umount"])
	}
}

func TestMountAssistantBatchSettings(t *testing.T) {
	env := Env{
		Mounts: func() ([]MountCandidate, error) {
			return []MountCandidate{
				{Path: "/mnt/a", Source: "/dev/a", FSType: "ext4"},
				{Path: "/mnt/b", Source: "/dev/b", FSType: "xfs"},
			}, nil
		},
	}
	p := NewPrompt(strings.NewReader("all\ny\nn\n"), &strings.Builder{})
	res, err := (mountAssistant{}).Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(res.Mounts) != 2 {
		t.Fatalf("mounts = %+v, want two", res.Mounts)
	}
	for name, raw := range res.Mounts {
		body := raw.(map[string]any)
		mount := body["mount"].(map[string]any)
		if mount["refcount"] != false {
			t.Fatalf("%s refcount = %v, want false", name, mount["refcount"])
		}
	}
}

func TestMountAssistantNoCandidates(t *testing.T) {
	env := Env{Mounts: func() ([]MountCandidate, error) { return nil, nil }}
	p := NewPrompt(strings.NewReader(""), &strings.Builder{})
	if _, err := (mountAssistant{}).Run(p, env); err == nil || !strings.Contains(err.Error(), "no fstab mount points") {
		t.Fatalf("Run error = %v, want no candidates", err)
	}
}
