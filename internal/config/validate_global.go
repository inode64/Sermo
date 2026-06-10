package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"sermo/internal/cfgval"
)

// validateWatches checks each host-watch entry: a known check type with valid
// thresholds and a non-empty hook command (spec 2026-06-06-host-watches-disk).
// validateWeb checks the global `web` block. The UI is enabled only when `port`
// is set to an integer in 1..65535; a `web` block without `port` (or with port
// omitted) is valid and leaves the dashboard disabled, matching sermod.
func validateWeb(webCfg map[string]any, add func(string, ...any)) {
	if portRaw, present := webCfg["port"]; present {
		port, ok := cfgval.Int(portRaw)
		if !ok || port < 1 || port > 65535 {
			add("web.port must be an integer in 1..65535")
		}
	}
	if v, present := webCfg["address"]; present {
		if _, isStr := v.(string); !isStr {
			add("web.address must be a string")
		}
	}
	for _, key := range []string{"password", "guest_password"} {
		if v, present := webCfg[key]; present {
			if _, isStr := v.(string); !isStr {
				add("web.%s must be a string", key)
			}
		}
	}
	if v, present := webCfg["guest"]; present {
		if _, isBool := v.(bool); !isBool {
			add("web.guest must be a boolean (allow anonymous read-only access)")
		}
	}
}

// validateNotifiers checks the global `notifiers` section: each entry is a known
// type with the fields that type needs. New transports validate here too.
func validateNotifiers(notifiers map[string]any, add func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(notifiers)) {
		entry, ok := notifiers[name].(map[string]any)
		if !ok {
			add("notifiers.%s must be a mapping", name)
			continue
		}
		switch cfgval.String(entry["type"]) {
		case "email":
			dsn := cfgval.String(entry["dsn"])
			if dsn == "" {
				add("notifiers.%s.dsn is required for an email notifier", name)
			} else if !strings.HasPrefix(dsn, "smtp://") && !strings.HasPrefix(dsn, "smtps://") {
				add("notifiers.%s.dsn must be an smtp:// or smtps:// URL", name)
			}
			if cfgval.String(entry["from"]) == "" {
				add("notifiers.%s.from is required for an email notifier", name)
			}
			if len(cfgval.StringList(entry["to"])) == 0 {
				add("notifiers.%s.to must list at least one address", name)
			}
		case "slack":
			wh := cfgval.String(entry["webhook"])
			if wh == "" {
				add("notifiers.%s.webhook is required for a slack notifier", name)
			} else if !strings.HasPrefix(wh, "http://") && !strings.HasPrefix(wh, "https://") {
				add("notifiers.%s.webhook must be an http(s) URL", name)
			}
		case "":
			add("notifiers.%s.type is required", name)
		default:
			add("notifiers.%s.type %q is not supported (email, slack)", name, cfgval.String(entry["type"]))
		}
	}
}

// notifierNames returns the set of defined notifier names, for reference checks.
func notifierNames(notifiers map[string]any) map[string]struct{} {
	names := make(map[string]struct{}, len(notifiers))
	for name := range notifiers {
		names[name] = struct{}{}
	}
	return names
}

// validateNotifyRefs checks that every `then.notify` name in a watch (entry-level
// and per-metric) refers to a defined notifier.
func validateNotifyRefs(name string, entry map[string]any, notifiers map[string]struct{}, add func(string, ...any)) {
	check := func(prefix string, then any) {
		t, ok := then.(map[string]any)
		if !ok {
			return
		}
		for _, ref := range cfgval.StringList(t["notify"]) {
			if _, ok := notifiers[ref]; !ok {
				add("%s.then.notify references unknown notifier %q", prefix, ref)
			}
		}
	}
	check("watches."+name, entry["then"])
	if metrics, ok := entry["metrics"].(map[string]any); ok {
		for _, key := range slices.Sorted(maps.Keys(metrics)) {
			if m, ok := metrics[key].(map[string]any); ok {
				check(fmt.Sprintf("watches.%s.metrics.%s", name, key), m["then"])
			}
		}
	}
}

func defaultsCooldown(defaults map[string]any) (string, bool) {
	policy, ok := defaults["policy"].(map[string]any)
	if !ok {
		return "", false
	}
	v, present := policy["cooldown"]
	if !present {
		return "", false
	}
	return cfgval.String(v), true
}

func isValidBackend(b string) bool {
	_, ok := validBackends[b]
	return ok
}
