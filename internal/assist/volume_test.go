package assist

import (
	"strings"
	"testing"
)

func testEnv() Env {
	return Env{
		Notifiers: []string{"ops-email", "team-slack"},
		Volumes: func() ([]Volume, error) {
			return []Volume{
				{Mountpoint: "/mnt/backup", FSType: "ext4", Device: "/dev/mapper/vg0-data"},
				{Mountpoint: "/", FSType: "xfs", Device: "/dev/sda1"},
			}, nil
		},
		Ifaces: func() ([]Iface, error) {
			return []Iface{
				{Name: "eth0", Up: true},
				{Name: "lo", Up: true, Loopback: true},
			}, nil
		},
	}
}

func TestVolumeAssistantFreePctWithExpand(t *testing.T) {
	// Select volume 1 (/mnt/backup); free space condition, 10%; for 3 cycles;
	// notifier ops-email; enable expand 5G cooldown 30m.
	script := strings.Join([]string{
		"1",   // MultiChoose volumes -> /mnt/backup
		"1",   // condition: free space below %
		"10",  // value
		"3",   // for cycles
		"2",   // notifier ops-email
		"y",   // auto-expand
		"5G",  // by
		"30m", // cooldown
	}, "\n") + "\n"

	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry, ok := res.Watches["disk-mnt-backup"].(map[string]any)
	if !ok {
		t.Fatalf("expected watch disk-mnt-backup, got %v", res.Watches)
	}
	check := entry["check"].(map[string]any)
	if check["type"] != "disk" || check["path"] != "/mnt/backup" {
		t.Fatalf("check = %v", check)
	}
	fp := check["free_pct"].(map[string]any)
	if fp["op"] != "<" || fp["value"] != 10 {
		t.Fatalf("free_pct = %v, want op< value10", fp)
	}
	then := entry["then"].(map[string]any)
	notify := then["notify"].([]string)
	if len(notify) != 1 || notify[0] != "ops-email" {
		t.Fatalf("notify = %v", notify)
	}
	exp := then["expand"].(map[string]any)
	if exp["by"] != "5G" {
		t.Fatalf("expand by = %v", exp["by"])
	}
	if entry["policy"].(map[string]any)["cooldown"] != "30m" {
		t.Fatalf("policy = %v", entry["policy"])
	}
	if entry["for"].(map[string]any)["cycles"] != 3 {
		t.Fatalf("for = %v", entry["for"])
	}
}

func TestVolumeAssistantUsedPctNoExpand(t *testing.T) {
	// Select volume 2 (/), used-space condition 90, for 1, notifier team-slack, no expand.
	script := strings.Join([]string{"2", "2", "90", "1", "3", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["disk-root"].(map[string]any)
	check := entry["check"].(map[string]any)
	up := check["used_pct"].(map[string]any)
	if up["op"] != ">=" || up["value"] != 90 {
		t.Fatalf("used_pct = %v", up)
	}
	then := entry["then"].(map[string]any)
	if _, hasExpand := then["expand"]; hasExpand {
		t.Fatalf("must not have expand: %v", then)
	}
	if _, hasPolicy := entry["policy"]; hasPolicy {
		t.Fatalf("no policy without expand: %v", entry)
	}
}

func TestVolumeAssistantNoActionErrors(t *testing.T) {
	env := testEnv()
	env.Notifiers = nil // no notifiers configured
	// Select volume 1; free 10; for 3; (no notifier prompt); decline expand.
	script := strings.Join([]string{"1", "1", "10", "3", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	if _, err := (volumeAssistant{}).Run(p, env); err == nil {
		t.Fatal("a watch with neither notify nor expand must error")
	}
}

func TestVolumeAssistantNotifyNoneWithExpand(t *testing.T) {
	// Select volume 1; free 10; for 3; explicit none; enable expand.
	script := strings.Join([]string{"1", "1", "10", "3", "1", "y", "5G", "30m"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["disk-mnt-backup"].(map[string]any)
	then := entry["then"].(map[string]any)
	notify := then["notify"].([]string)
	if len(notify) != 1 || notify[0] != "none" {
		t.Fatalf("notify = %v, want [none]", notify)
	}
	if _, ok := then["expand"].(map[string]any); !ok {
		t.Fatalf("expand missing from then: %v", then)
	}
}

func TestVolumeAssistantNotifyNoneWithoutExpandErrors(t *testing.T) {
	// Select volume 1; free 10; for 3; explicit none; decline expand.
	script := strings.Join([]string{"1", "1", "10", "3", "1", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	if _, err := (volumeAssistant{}).Run(p, testEnv()); err == nil {
		t.Fatal("notify none without expand should leave no action and error")
	}
}
