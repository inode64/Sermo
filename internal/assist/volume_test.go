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

func testEnvWithDefaultNotify() Env {
	env := testEnv()
	env.DefaultNotify = []string{"ops-email"}
	return env
}

func TestVolumeAssistantFreePctWithExpand(t *testing.T) {
	// Select volume 1 (/mnt/backup); free space condition, 10%; for 3 cycles;
	// notifier ops-email; enable expand 5G cooldown 30m.
	script := strings.Join([]string{
		"1",   // MultiChoose volumes -> /mnt/backup
		"1",   // condition: free space below %
		"10",  // value
		"3",   // for cycles
		"1",   // notifier ops-email
		"y",   // auto-expand
		"5G",  // by
		"30m", // cooldown
	}, "\n") + "\n"

	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry, ok := res.Watches["storage-mnt-backup"].(map[string]any)
	if !ok {
		t.Fatalf("expected watch storage-mnt-backup, got %v", res.Watches)
	}
	check := entry["check"].(map[string]any)
	if check["type"] != "storage" || check["path"] != "/mnt/backup" {
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
	script := strings.Join([]string{"2", "2", "90", "1", "2", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-root"].(map[string]any)
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

func TestVolumeAssistantPercentSuffix(t *testing.T) {
	// Select volume 2 (/), used-space condition 90%, for 1, notifier team-slack, no expand.
	script := strings.Join([]string{"2", "2", "90%", "1", "2", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-root"].(map[string]any)
	check := entry["check"].(map[string]any)
	up := check["used_pct"].(map[string]any)
	if up["op"] != ">=" || up["value"] != "90%" {
		t.Fatalf("used_pct = %v", up)
	}
}

func TestVolumeAssistantFreeBytesNoExpand(t *testing.T) {
	// Select volume 1; free-space size condition 10G; for 2; notifier ops-email.
	script := strings.Join([]string{"1", "3", "10G", "2", "1", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	check := entry["check"].(map[string]any)
	free := check["free_bytes"].(map[string]any)
	if free["op"] != "<" || free["value"] != "10G" {
		t.Fatalf("free_bytes = %v", free)
	}
}

func TestVolumeAssistantSizeRequiresSuffix(t *testing.T) {
	// Select volume 1; size condition first tries unitless 100, then valid 100G.
	script := strings.Join([]string{"1", "4", "100", "100G", "2", "1", "n"}, "\n") + "\n"
	var out strings.Builder
	p := NewPrompt(strings.NewReader(script), &out)
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	check := entry["check"].(map[string]any)
	used := check["used_bytes"].(map[string]any)
	if used["value"] != "100G" {
		t.Fatalf("used_bytes = %v", used)
	}
	if !strings.Contains(out.String(), "use a size like 5G, 500M or 2T") {
		t.Fatalf("expected suffix prompt after unitless size, got %q", out.String())
	}
}

func TestVolumeAssistantUsedBytesNoExpand(t *testing.T) {
	// Select volume 1; used-space size condition 100G; for 2; notifier ops-email.
	script := strings.Join([]string{"1", "4", "100G", "2", "1", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	check := entry["check"].(map[string]any)
	used := check["used_bytes"].(map[string]any)
	if used["op"] != ">=" || used["value"] != "100G" {
		t.Fatalf("used_bytes = %v", used)
	}
}

func TestVolumeAssistantInheritsGlobalNotify(t *testing.T) {
	// Select volume 1; free 10; for 3; inherit global notify; no expand.
	script := strings.Join([]string{"1", "1", "10", "3", "default", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnvWithDefaultNotify())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	then := entry["then"].(map[string]any)
	if _, hasNotify := then["notify"]; hasNotify {
		t.Fatalf("notify should be omitted to inherit global default: %v", then)
	}
	if _, hasExpand := then["expand"]; hasExpand {
		t.Fatalf("expand should not be present: %v", then)
	}
}

func TestVolumeAssistantNoActionErrors(t *testing.T) {
	env := testEnv()
	env.Notifiers = nil // no notifiers configured
	// Select volume 1; free 10; for 3; default notify (not configured); decline
	// expand: the wizard re-asks and the script's EOF aborts with ErrInputClosed.
	script := strings.Join([]string{"1", "1", "10", "3", "default", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	if _, err := (volumeAssistant{}).Run(p, env); err == nil {
		t.Fatal("a watch with neither notify nor expand must error")
	}
}

func TestVolumeAssistantNoneWithoutExpandMonitorOnly(t *testing.T) {
	// 'none' with expand declined is the reserved monitor-only opt-out: it is
	// accepted directly, with no re-ask.
	script := strings.Join([]string{"1", "1", "10", "3", "none", "n"}, "\n") + "\n"
	var out strings.Builder
	p := NewPrompt(strings.NewReader(script), &out)
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	then := res.Watches["storage-mnt-backup"].(map[string]any)["then"].(map[string]any)
	notify := then["notify"].([]string)
	if len(notify) != 1 || notify[0] != "none" {
		t.Fatalf("notify = %v, want [none]", notify)
	}
	if strings.Contains(out.String(), "would leave this watch with no action") {
		t.Fatalf("'none' must not re-ask, got %q", out.String())
	}
}

func TestVolumeAssistantDefaultWithoutGlobalWithExpand(t *testing.T) {
	env := testEnv()
	env.Notifiers = nil
	// Select volume 1; free 10; for 3; default notify (not configured); enable expand.
	script := strings.Join([]string{"1", "1", "10", "3", "default", "y", "5G", "30m"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	then := entry["then"].(map[string]any)
	if _, hasNotify := then["notify"]; hasNotify {
		t.Fatalf("default should omit notify even without a configured global default: %v", then)
	}
	if _, ok := then["expand"].(map[string]any); !ok {
		t.Fatalf("expand missing from then: %v", then)
	}
}

func TestVolumeAssistantNotifyNoneWithExpand(t *testing.T) {
	// Select volume 1; free 10; for 3; explicit none; enable expand.
	script := strings.Join([]string{"1", "1", "10", "3", "none", "y", "5G", "30m"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	then := entry["then"].(map[string]any)
	notify := then["notify"].([]string)
	if len(notify) != 1 || notify[0] != "none" {
		t.Fatalf("notify = %v, want [none]", notify)
	}
	if _, ok := then["expand"].(map[string]any); !ok {
		t.Fatalf("expand missing from then: %v", then)
	}
}

func TestVolumeAssistantNotifyKeywordsWithoutNotifiers(t *testing.T) {
	// With no notifiers configured, "none" and "default" must still be
	// selectable (by name), so an expand-only watch can opt out or inherit.
	base := testEnv()
	base.Notifiers = nil // no notifiers defined in the config

	t.Run("none", func(t *testing.T) {
		// Select volume 1; free 10; for 3; type "none"; enable expand.
		script := strings.Join([]string{"1", "1", "10", "3", "none", "y", "5G", "30m"}, "\n") + "\n"
		p := NewPrompt(strings.NewReader(script), &strings.Builder{})
		res, err := volumeAssistant{}.Run(p, base)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		then := res.Watches["storage-mnt-backup"].(map[string]any)["then"].(map[string]any)
		notify := then["notify"].([]string)
		if len(notify) != 1 || notify[0] != "none" {
			t.Fatalf("notify = %v, want [none]", notify)
		}
	})

	t.Run("default", func(t *testing.T) {
		// Select volume 1; free 10; for 3; type "default"; enable expand.
		script := strings.Join([]string{"1", "1", "10", "3", "default", "y", "5G", "30m"}, "\n") + "\n"
		p := NewPrompt(strings.NewReader(script), &strings.Builder{})
		res, err := volumeAssistant{}.Run(p, base)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		then := res.Watches["storage-mnt-backup"].(map[string]any)["then"].(map[string]any)
		if _, hasNotify := then["notify"]; hasNotify {
			t.Fatalf("'default' should omit notify to inherit the global default: %v", then)
		}
	})
}
