package assist

import (
	"strings"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
)

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
		"",                          // escalate to error: default
		"",                          // escalate to critical: default
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
	entry := runVolumeAssistant(t, testEnv(), "storage-root", "2", "1", "", "2", "90", "", "", "1", "2", "n", "n")
	assertCheckPred(t, entry, checks.LevelFieldUsedPct, cfgval.CompareOpGreaterEqual, 90)
	then := entryThen(entry)
	if _, hasExpand := then[config.WatchThenKeyExpand]; hasExpand {
		t.Fatalf("must not have expand: %v", then)
	}
	if _, hasPolicy := entry[rules.SectionPolicy]; hasPolicy {
		t.Fatalf("no policy without expand: %v", entry)
	}
}

func TestVolumeAssistantPercentSuffix(t *testing.T) {
	// Select volume 2 (/), used-space condition 90%, for 1, notifier team-slack, no expand.
	entry := runVolumeAssistant(t, testEnv(), "storage-root", "2", "1", "", "2", "90%", "", "", "1", "2", "n", "n")
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
		"1", "1", "", "1", "10", "", "", "3", config.NotifyKeywordDefault, "n", "n")
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
	script := strings.Join([]string{"1", "1", "", "1", "10", "", "", "3", config.NotifyKeywordDefault, "n"}, "\n") + "\n"
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
		"1", "1", "", "1", "10", "", "", "3", config.NotifyNone, "n")
	assertNotifyNone(t, entryThen(entry))
}

// assertNotifyNoneWithExpand runs a free-10-for-3 wizard script with the given
// notify answer plus expand enabled, asserting a monitor-only notify with the
// expand action still attached.
func assertNotifyNoneWithExpand(t *testing.T, env Env, notifyAnswer string) {
	t.Helper()
	entry := runVolumeAssistant(t, env, "storage-mnt-backup",
		"1", "1", "", "1", "10", "", "", "3", notifyAnswer, "y", volumeDefaultExpandBy, volumeDefaultExpandCooldown, "n")
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

// A percentage condition is graded: the base threshold warns, and the wizard
// asks where it escalates to error and critical, refusing a level that is not
// stricter than the one below it.
func TestVolumeAssistantGradesPercentConditions(t *testing.T) {
	// used 80 (warning); error first tries 70 (not stricter), then 95; critical 99.
	entry := runVolumeAssistant(t, testEnv(), "storage-root", "2", "1", "", "2", "80", "70", "95", "99", "1", "2", "n", "n")
	check := entry[config.WatchKeyCheck].(map[string]any)
	if check[checks.CheckKeySeverity] != "warning" {
		t.Fatalf("severity = %v, want warning", check[checks.CheckKeySeverity])
	}
	levels := check[checks.CheckKeyLevels].(map[string]any)
	for level, want := range map[string]any{"error": 95, "critical": 99} {
		pred := levels[level].(map[string]any)[checks.LevelFieldUsedPct].(map[string]any)
		if pred[checks.CheckKeyOp] != cfgval.CompareOpGreaterEqual || pred[checks.CheckKeyValue] != want {
			t.Fatalf("levels.%s = %v, want >= %v", level, pred, want)
		}
	}
	// A size condition keeps a single threshold.
	sized := runVolumeAssistant(t, testEnv(), "storage-mnt-backup", "1", "1", "", "3", volumeDefaultFreeSize, "2", "1", "n", "n")
	if _, graded := sized[config.WatchKeyCheck].(map[string]any)[checks.CheckKeyLevels]; graded {
		t.Fatal("a size condition was graded")
	}
}

// A tier that cannot tighten the one below it is not asked, and closed input
// ends the re-prompt loop instead of spinning.
func TestVolumeAssistantLevelEdges(t *testing.T) {
	// used 100 %: nothing is stricter, so the condition keeps one threshold —
	// still the warning the prompt asked for.
	entry := runVolumeAssistant(t, testEnv(), "storage-root", "2", "1", "", "2", "100", "1", "2", "n", "n")
	check := entry[config.WatchKeyCheck].(map[string]any)
	if _, graded := check[checks.CheckKeyLevels]; graded || check[checks.CheckKeySeverity] != "warning" {
		t.Fatalf("check = %v, want a single warning threshold", check)
	}
	// used 99.5 %: error at 100 %, no room left for critical.
	entry = runVolumeAssistant(t, testEnv(), "storage-root", "2", "1", "", "2", "99.5%", "100", "1", "2", "n", "n")
	levels := entry[config.WatchKeyCheck].(map[string]any)[checks.CheckKeyLevels].(map[string]any)
	if levels["error"] == nil || levels["critical"] != nil {
		t.Fatalf("levels = %v, want only an error tier", levels)
	}
	// used 96 %: the ladder's error default (95) is not stricter, so the
	// wizard offers the nearest one that is.
	entry = runVolumeAssistant(t, testEnv(), "storage-root", "2", "1", "", "2", "96", "", "", "1", "2", "n", "n")
	levels = entry[config.WatchKeyCheck].(map[string]any)[checks.CheckKeyLevels].(map[string]any)
	for level, want := range map[string]any{"error": 97, "critical": 99} {
		if got := levels[level].(map[string]any)[checks.LevelFieldUsedPct].(map[string]any)[checks.CheckKeyValue]; got != want {
			t.Fatalf("levels.%s = %v, want %v", level, got, want)
		}
	}
	// A level that is not stricter, then EOF: the closed input must abort
	// rather than loop forever.
	p := NewPrompt(strings.NewReader(strings.Join([]string{"2", "1", "", "2", "96", "90"}, "\n")+"\n"), &strings.Builder{})
	if _, err := (volumeAssistant{}).Run(p, testEnv()); err == nil {
		t.Fatal("closed input produced a result, want an input-closed error")
	}
}
