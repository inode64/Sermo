package assist

import (
	"strings"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
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
		"1",                         // MultiChoose volumes -> /mnt/backup
		"1",                         // monitor state: enabled
		"",                          // interval: inherit global
		"1",                         // condition: free space below %
		"10",                        // value
		"3",                         // for cycles
		"1",                         // notifier ops-email
		"y",                         // auto-expand
		volumeDefaultExpandBy,       // by
		volumeDefaultExpandCooldown, // cooldown
		"y",                         // dry-run actions first
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
	check := entry[config.WatchKeyCheck].(map[string]any)
	if check[checks.CheckKeyType] != checks.CheckTypeStorage || check[checks.CheckKeyPath] != "/mnt/backup" {
		t.Fatalf("check = %v", check)
	}
	if entry[config.EntryKeyCategory] != config.WatchCategoryStorage {
		t.Fatalf("category = %v, want storage", entry[config.EntryKeyCategory])
	}
	fp := check[checks.LevelFieldFreePct].(map[string]any)
	if fp[checks.CheckKeyOp] != cfgval.CompareOpLess || fp[checks.CheckKeyValue] != 10 {
		t.Fatalf("free_pct = %v, want op< value10", fp)
	}
	then := entry[config.WatchKeyThen].(map[string]any)
	notify := then[rules.RuleFieldNotify].([]string)
	if len(notify) != 1 || notify[0] != "ops-email" {
		t.Fatalf("notify = %v", notify)
	}
	exp := then[config.WatchThenKeyExpand].(map[string]any)
	if exp[config.WatchExpandKeyBy] != volumeDefaultExpandBy {
		t.Fatalf("expand by = %v", exp[config.WatchExpandKeyBy])
	}
	if entry[config.EntryKeyDryRun] != true {
		t.Fatalf("dry_run = %v, want true", entry[config.EntryKeyDryRun])
	}
	if entry[rules.SectionPolicy].(map[string]any)[rules.PolicyKeyCooldown] != volumeDefaultExpandCooldown {
		t.Fatalf("policy = %v", entry[rules.SectionPolicy])
	}
	if entry[rules.RuleFieldFor].(map[string]any)[rules.WindowKeyCycles] != volumeDefaultForCycles {
		t.Fatalf("for = %v", entry[rules.RuleFieldFor])
	}
}

func TestVolumeAssistantUsedPctNoExpand(t *testing.T) {
	// Select volume 2 (/), used-space condition 90, for 1, notifier team-slack, no expand.
	script := strings.Join([]string{"2", "1", "", "2", "90", "1", "2", "n", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-root"].(map[string]any)
	check := entry[config.WatchKeyCheck].(map[string]any)
	up := check[checks.LevelFieldUsedPct].(map[string]any)
	if up[checks.CheckKeyOp] != cfgval.CompareOpGreaterEqual || up[checks.CheckKeyValue] != volumeDefaultUsedPct {
		t.Fatalf("used_pct = %v", up)
	}
	then := entry[config.WatchKeyThen].(map[string]any)
	if _, hasExpand := then[config.WatchThenKeyExpand]; hasExpand {
		t.Fatalf("must not have expand: %v", then)
	}
	if _, hasPolicy := entry[rules.SectionPolicy]; hasPolicy {
		t.Fatalf("no policy without expand: %v", entry)
	}
}

func TestVolumeAssistantSkipsRPCPipeFS(t *testing.T) {
	env := testEnv()
	env.Volumes = func() ([]Volume, error) {
		return []Volume{
			{Mountpoint: "/var/lib/nfs/rpc_pipefs", FSType: "rpc_pipefs", Device: "rpc_pipefs"},
			{Mountpoint: "/srv/data", FSType: "ext4", Device: "/dev/sdb1"},
		}, nil
	}

	script := strings.Join([]string{"1", "1", "", "1", "10", "3", config.NotifyNone, "n"}, "\n") + "\n"
	var out strings.Builder
	p := NewPrompt(strings.NewReader(script), &out)
	res, err := volumeAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(out.String(), "rpc_pipefs") {
		t.Fatalf("rpc_pipefs should not be offered by the wizard:\n%s", out.String())
	}
	if _, ok := res.Watches["storage-srv-data"]; !ok {
		t.Fatalf("expected storage-srv-data watch, got %v", res.Watches)
	}
	if _, ok := res.Watches["storage-var-lib-nfs-rpc-pipefs"]; ok {
		t.Fatalf("rpc_pipefs watch must not be generated: %v", res.Watches)
	}
}

func TestVolumeAssistantPercentSuffix(t *testing.T) {
	// Select volume 2 (/), used-space condition 90%, for 1, notifier team-slack, no expand.
	script := strings.Join([]string{"2", "1", "", "2", "90%", "1", "2", "n", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-root"].(map[string]any)
	check := entry[config.WatchKeyCheck].(map[string]any)
	up := check[checks.LevelFieldUsedPct].(map[string]any)
	if up[checks.CheckKeyOp] != cfgval.CompareOpGreaterEqual || up[checks.CheckKeyValue] != "90%" {
		t.Fatalf("used_pct = %v", up)
	}
}

func TestVolumeAssistantFreeBytesNoExpand(t *testing.T) {
	// Select volume 1; free-space size condition 10G; for 2; notifier ops-email.
	script := strings.Join([]string{"1", "1", "", "3", volumeDefaultFreeSize, "2", "1", "n", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	check := entry[config.WatchKeyCheck].(map[string]any)
	free := check[checks.LevelFieldFreeBytes].(map[string]any)
	if free[checks.CheckKeyOp] != cfgval.CompareOpLess || free[checks.CheckKeyValue] != volumeDefaultFreeSize {
		t.Fatalf("free_bytes = %v", free)
	}
}

func TestVolumeAssistantSizeRequiresSuffix(t *testing.T) {
	// Select volume 1; size condition first tries unitless 100, then valid 100G.
	script := strings.Join([]string{"1", "1", "", "4", "100", volumeDefaultUsedSize, "2", "1", "n", "n"}, "\n") + "\n"
	var out strings.Builder
	p := NewPrompt(strings.NewReader(script), &out)
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	check := entry[config.WatchKeyCheck].(map[string]any)
	used := check[checks.LevelFieldUsedBytes].(map[string]any)
	if used[checks.CheckKeyValue] != volumeDefaultUsedSize {
		t.Fatalf("used_bytes = %v", used)
	}
	if !strings.Contains(out.String(), "use a size like 5G, 500M or 2T") {
		t.Fatalf("expected suffix prompt after unitless size, got %q", out.String())
	}
}

func TestVolumeAssistantUsedBytesNoExpand(t *testing.T) {
	// Select volume 1; used-space size condition 100G; for 2; notifier ops-email.
	script := strings.Join([]string{"1", "1", "", "4", volumeDefaultUsedSize, "2", "1", "n", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	check := entry[config.WatchKeyCheck].(map[string]any)
	used := check[checks.LevelFieldUsedBytes].(map[string]any)
	if used[checks.CheckKeyOp] != cfgval.CompareOpGreaterEqual || used[checks.CheckKeyValue] != volumeDefaultUsedSize {
		t.Fatalf("used_bytes = %v", used)
	}
}

func TestVolumeAssistantInheritsGlobalNotify(t *testing.T) {
	// Select volume 1; monitor enabled; inherit interval; free 10; for 3; inherit
	// global notify; no expand.
	script := strings.Join([]string{"1", "1", "", "1", "10", "3", config.NotifyKeywordDefault, "n", "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnvWithDefaultNotify())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	then := entry[config.WatchKeyThen].(map[string]any)
	if _, hasNotify := then[rules.RuleFieldNotify]; hasNotify {
		t.Fatalf("notify should be omitted to inherit global default: %v", then)
	}
	if _, hasExpand := then[config.WatchThenKeyExpand]; hasExpand {
		t.Fatalf("expand should not be present: %v", then)
	}
}

func TestVolumeAssistantDefaultWithoutGlobalMonitorOnly(t *testing.T) {
	env := testEnv()
	env.Notifiers = nil // no notifiers configured, and no global notify default
	// Select volume 1; monitor enabled; inherit interval; free 10; for 3; default
	// notify (not configured); decline expand. 'default' is accepted and degrades
	// to a monitor-only watch (notify [none]) instead of erroring or re-asking.
	script := strings.Join([]string{"1", "1", "", "1", "10", "3", config.NotifyKeywordDefault, "n"}, "\n") + "\n"
	var out strings.Builder
	p := NewPrompt(strings.NewReader(script), &out)
	res, err := volumeAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	notify := res.Watches["storage-mnt-backup"].(map[string]any)[config.WatchKeyThen].(map[string]any)[rules.RuleFieldNotify].([]string)
	if len(notify) != 1 || notify[0] != config.NotifyNone {
		t.Fatalf("notify = %v, want [none] (monitor-only)", notify)
	}
	if !strings.Contains(out.String(), "monitor-only") {
		t.Fatalf("expected the monitor-only note, got %q", out.String())
	}
}

func TestVolumeAssistantNoneWithoutExpandMonitorOnly(t *testing.T) {
	// 'none' with expand declined is the reserved monitor-only opt-out: it is
	// accepted directly.
	script := strings.Join([]string{"1", "1", "", "1", "10", "3", config.NotifyNone, "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	then := res.Watches["storage-mnt-backup"].(map[string]any)[config.WatchKeyThen].(map[string]any)
	notify := then[rules.RuleFieldNotify].([]string)
	if len(notify) != 1 || notify[0] != config.NotifyNone {
		t.Fatalf("notify = %v, want [none]", notify)
	}
}

func TestVolumeAssistantDefaultWithoutGlobalWithExpand(t *testing.T) {
	env := testEnv()
	env.Notifiers = nil
	// Select volume 1; monitor enabled; inherit interval; free 10; for 3; default
	// notify (not configured); enable expand. default degrades to monitor-only
	// (notify [none]); the expand action is still attached.
	script := strings.Join([]string{"1", "1", "", "1", "10", "3", config.NotifyKeywordDefault, "y", volumeDefaultExpandBy, volumeDefaultExpandCooldown, "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	then := res.Watches["storage-mnt-backup"].(map[string]any)[config.WatchKeyThen].(map[string]any)
	notify := then[rules.RuleFieldNotify].([]string)
	if len(notify) != 1 || notify[0] != config.NotifyNone {
		t.Fatalf("notify = %v, want [none] (monitor-only)", notify)
	}
	if _, ok := then[config.WatchThenKeyExpand].(map[string]any); !ok {
		t.Fatalf("expand missing from then: %v", then)
	}
}

func TestVolumeAssistantNotifyNoneWithExpand(t *testing.T) {
	// Select volume 1; monitor enabled; inherit interval; free 10; for 3; explicit
	// none; enable expand.
	script := strings.Join([]string{"1", "1", "", "1", "10", "3", config.NotifyNone, "y", volumeDefaultExpandBy, volumeDefaultExpandCooldown, "n"}, "\n") + "\n"
	p := NewPrompt(strings.NewReader(script), &strings.Builder{})
	res, err := volumeAssistant{}.Run(p, testEnv())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	entry := res.Watches["storage-mnt-backup"].(map[string]any)
	then := entry[config.WatchKeyThen].(map[string]any)
	notify := then[rules.RuleFieldNotify].([]string)
	if len(notify) != 1 || notify[0] != config.NotifyNone {
		t.Fatalf("notify = %v, want [none]", notify)
	}
	if _, ok := then[config.WatchThenKeyExpand].(map[string]any); !ok {
		t.Fatalf("expand missing from then: %v", then)
	}
}

func TestVolumeAssistantNotifyKeywordsWithoutNotifiers(t *testing.T) {
	// With no notifiers configured, "none" and "default" must still be
	// selectable (by name), so an expand-only watch can opt out or inherit.
	base := testEnv()
	base.Notifiers = nil // no notifiers defined in the config

	t.Run("none", func(t *testing.T) {
		// Select volume 1; monitor enabled; inherit interval; free 10; for 3; type
		// "none"; enable expand.
		script := strings.Join([]string{"1", "1", "", "1", "10", "3", config.NotifyNone, "y", volumeDefaultExpandBy, volumeDefaultExpandCooldown, "n"}, "\n") + "\n"
		p := NewPrompt(strings.NewReader(script), &strings.Builder{})
		res, err := volumeAssistant{}.Run(p, base)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		then := res.Watches["storage-mnt-backup"].(map[string]any)[config.WatchKeyThen].(map[string]any)
		notify := then[rules.RuleFieldNotify].([]string)
		if len(notify) != 1 || notify[0] != config.NotifyNone {
			t.Fatalf("notify = %v, want [none]", notify)
		}
	})

	t.Run("default", func(t *testing.T) {
		// With no notifiers and no global default, "default" is still selectable
		// and degrades to monitor-only (notify [none]).
		script := strings.Join([]string{"1", "1", "", "1", "10", "3", config.NotifyKeywordDefault, "y", volumeDefaultExpandBy, volumeDefaultExpandCooldown, "n"}, "\n") + "\n"
		p := NewPrompt(strings.NewReader(script), &strings.Builder{})
		res, err := volumeAssistant{}.Run(p, base)
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		then := res.Watches["storage-mnt-backup"].(map[string]any)[config.WatchKeyThen].(map[string]any)
		notify := then[rules.RuleFieldNotify].([]string)
		if len(notify) != 1 || notify[0] != config.NotifyNone {
			t.Fatalf("notify = %v, want [none] (monitor-only)", notify)
		}
	})
}
