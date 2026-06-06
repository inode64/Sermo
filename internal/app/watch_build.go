package app

import (
	"fmt"
	"sort"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/rules"
)

// BuildWatches resolves the global `watches` section into runnable Watches.
// Disabled or malformed entries are skipped with a warning (like BuildWorkers).
func BuildWatches(cfg *config.Config, deps Deps, defaultInterval time.Duration) ([]*Watch, []string) {
	raw, ok := cfg.Global.Raw["watches"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil, nil
	}

	var watches []*Watch
	var warnings []string
	for _, name := range sortedWatchNames(raw) {
		entry, ok := raw[name].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+" is not a mapping")
			continue
		}
		if isDisabled(entry) {
			continue
		}

		checkEntry, ok := entry["check"].(map[string]any)
		if !ok {
			warnings = append(warnings, "watch "+name+": missing check")
			continue
		}
		check, err := checks.BuildInline(name, checkEntry, checks.Deps{
			DefaultTimeout: deps.DefaultTimeout,
			DiskUsage:      nil, // statfs default
		})
		if err != nil {
			warnings = append(warnings, "watch "+name+": "+err.Error())
			continue
		}

		hook, err := parseHook(entry)
		if err != nil {
			warnings = append(warnings, "watch "+name+": "+err.Error())
			continue
		}

		interval := defaultInterval
		if d := durationField(entry["interval"]); d > 0 {
			interval = d
		}

		watches = append(watches, &Watch{
			Name:      name,
			CheckType: stringField(checkEntry["type"]),
			Check:     check,
			Window:    rules.Rule{For: parseForField(entry["for"]), Within: parseWithinField(entry["within"])},
			Hook:      hook,
			Runner:    OSHookRunner{},
			Interval:  interval,
			Now:       deps.Now,
			Emit:      deps.Emit,
		})
	}
	return watches, warnings
}

func parseHook(entry map[string]any) (HookSpec, error) {
	then, ok := entry["then"].(map[string]any)
	if !ok {
		return HookSpec{}, fmt.Errorf("missing then")
	}
	hook, ok := then["hook"].(map[string]any)
	if !ok {
		return HookSpec{}, fmt.Errorf("then has no hook")
	}
	cmd := stringArray(hook["command"])
	if len(cmd) == 0 {
		return HookSpec{}, fmt.Errorf("hook requires a non-empty command")
	}
	return HookSpec{Command: cmd, Timeout: durationField(hook["timeout"])}, nil
}

func parseForField(v any) *rules.ForWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &rules.ForWindow{Cycles: intField(m["cycles"]), Mode: stringField(m["mode"])}
}

func parseWithinField(v any) *rules.WithinWindow {
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	return &rules.WithinWindow{Cycles: intField(m["cycles"]), MinMatches: intField(m["min_matches"])}
}

func sortedWatchNames(m map[string]any) []string {
	names := make([]string, 0, len(m))
	for k := range m {
		names = append(names, k)
	}
	sort.Strings(names)
	return names
}

func stringArray(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s, ok := e.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

func durationField(v any) time.Duration {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}

func intField(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case uint64:
		return int(t)
	case float64:
		return int(t)
	default:
		return 0
	}
}

func stringField(v any) string {
	s, _ := v.(string)
	return s
}
