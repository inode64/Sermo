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

// runAssistant drives assistant a with the newline-joined script steps against
// env and returns the result plus captured output.
func runAssistant(t *testing.T, a Assistant, env Env, steps ...string) (Result, string) {
	t.Helper()
	var out strings.Builder
	p := NewPrompt(strings.NewReader(strings.Join(steps, "\n")+"\n"), &out)
	res, err := a.Run(p, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res, out.String()
}

// runVolumeAssistant drives the volume wizard with the newline-joined script
// steps against env and returns the produced watch entry.
func runVolumeAssistant(t *testing.T, env Env, watch string, steps ...string) map[string]any {
	t.Helper()
	res, _ := runAssistant(t, volumeAssistant{}, env, steps...)
	entry, ok := res.Watches[watch].(map[string]any)
	if !ok {
		t.Fatalf("expected watch %s, got %v", watch, res.Watches)
	}
	return entry
}

// assertCheckPred asserts the entry check's field predicate op and value.
func assertCheckPred(t *testing.T, entry map[string]any, field, op string, value any) {
	t.Helper()
	pred := entry[config.WatchKeyCheck].(map[string]any)[field].(map[string]any)
	if pred[checks.CheckKeyOp] != op || pred[checks.CheckKeyValue] != value {
		t.Fatalf("%s = %v", field, pred)
	}
}

// entryThen returns the entry's then block.
func entryThen(entry map[string]any) map[string]any {
	return entry[config.WatchKeyThen].(map[string]any)
}

// assertNotifyNone asserts the then block's notify is exactly [none].
func assertNotifyNone(t *testing.T, then map[string]any) {
	t.Helper()
	notify := then[rules.RuleFieldNotify].([]string)
	if len(notify) != 1 || notify[0] != config.NotifyNone {
		t.Fatalf("notify = %v, want [none]", notify)
	}
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
	entry := runVolumeAssistant(t, testEnv(), "storage-root", "2", "1", "", "2", "90", "1", "2", "n", "n")
	assertCheckPred(t, entry, checks.LevelFieldUsedPct, cfgval.CompareOpGreaterEqual, volumeDefaultUsedPct)
	then := entryThen(entry)
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
	entry := runVolumeAssistant(t, testEnv(), "storage-root", "2", "1", "", "2", "90%", "1", "2", "n", "n")
	assertCheckPred(t, entry, checks.LevelFieldUsedPct, cfgval.CompareOpGreaterEqual, "90%")
}

func TestVolumeAssistantFreeBytesNoExpand(t *testing.T) {
	// Select volume 1; free-space size condition 10G; for 2; notifier ops-email.
	entry := runVolumeAssistant(t, testEnv(), "storage-mnt-backup", "1", "1", "", "3", volumeDefaultFreeSize, "2", "1", "n", "n")
	assertCheckPred(t, entry, checks.LevelFieldFreeBytes, cfgval.CompareOpLess, volumeDefaultFreeSize)
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
	entry := runVolumeAssistant(t, testEnv(), "storage-mnt-backup", "1", "1", "", "4", volumeDefaultUsedSize, "2", "1", "n", "n")
	assertCheckPred(t, entry, checks.LevelFieldUsedBytes, cfgval.CompareOpGreaterEqual, volumeDefaultUsedSize)
}

func TestVolumeAssistantInheritsGlobalNotify(t *testing.T) {
	// Select volume 1; monitor enabled; inherit interval; free 10; for 3; inherit
	// global notify; no expand.
	entry := runVolumeAssistant(t, testEnvWithDefaultNotify(), "storage-mnt-backup",
		"1", "1", "", "1", "10", "3", config.NotifyKeywordDefault, "n", "n")
	then := entryThen(entry)
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
	entry := runVolumeAssistant(t, testEnv(), "storage-mnt-backup",
		"1", "1", "", "1", "10", "3", config.NotifyNone, "n")
	assertNotifyNone(t, entryThen(entry))
}

// assertNotifyNoneWithExpand runs a free-10-for-3 wizard script with the given
// notify answer plus expand enabled, asserting a monitor-only notify with the
// expand action still attached.
func assertNotifyNoneWithExpand(t *testing.T, env Env, notifyAnswer string) {
	t.Helper()
	entry := runVolumeAssistant(t, env, "storage-mnt-backup",
		"1", "1", "", "1", "10", "3", notifyAnswer, "y", volumeDefaultExpandBy, volumeDefaultExpandCooldown, "n")
	then := entryThen(entry)
	assertNotifyNone(t, then)
	if _, ok := then[config.WatchThenKeyExpand].(map[string]any); !ok {
		t.Fatalf("expand missing from then: %v", then)
	}
}

func TestVolumeAssistantNotifyNoneWithExpand(t *testing.T) {
	// Select volume 1; monitor enabled; inherit interval; free 10; for 3; explicit
	// none; enable expand.
	assertNotifyNoneWithExpand(t, testEnv(), config.NotifyNone)
}

func TestVolumeAssistantNotifyKeywordsWithoutNotifiers(t *testing.T) {
	// With no notifiers configured, "none" and "default" must still be
	// selectable (by name), so an expand-only watch can opt out or inherit.
	base := testEnv()
	base.Notifiers = nil // no notifiers defined in the config

	t.Run("none", func(t *testing.T) {
		// Select volume 1; monitor enabled; inherit interval; free 10; for 3; type
		// "none"; enable expand.
		assertNotifyNoneWithExpand(t, base, config.NotifyNone)
	})

	t.Run("default", func(t *testing.T) {
		// With no notifiers and no global default, "default" is still selectable
		// and degrades to monitor-only (notify [none]).
		assertNotifyNoneWithExpand(t, base, config.NotifyKeywordDefault)
	})
}
