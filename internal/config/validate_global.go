package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"sermo/internal/cfgval"
)

// validateWatches checks each host-watch entry: a known check type with valid
// thresholds and a local action or inherited global notify default.
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
// notifyNone is the reserved notify sentinel: a notify selection of `none`
// suppresses delivery, and no notifier may take it as a name.
const notifyNone = "none"

func validateNotifiers(notifiers map[string]any, add func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(notifiers)) {
		if name == notifyNone {
			add("notifiers.%s: %q is a reserved keyword and cannot name a notifier", name, notifyNone)
			continue
		}
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

// NotifyDefault returns the global default notifier names from the top-level
// `notify` key: the listed names, or nil when the key is absent or set to the
// `none` sentinel. It is the fallback for any notify site that declares no
// selection of its own.
func NotifyDefault(raw map[string]any) []string {
	names := cfgval.StringList(raw["notify"])
	if slices.Contains(names, notifyNone) {
		return nil
	}
	return names
}

// validateNotifySelection validates a notify selection (a global `notify`, a
// watch `then.notify`, or a rule `notify`): every name must be a defined notifier
// or the `none` sentinel, and `none` cannot be combined with real names.
func validateNotifySelection(prefix string, names []string, defined map[string]struct{}, add func(string, ...any)) {
	if slices.Contains(names, notifyNone) && len(names) > 1 {
		add("%s: %q cannot be combined with notifier names", prefix, notifyNone)
	}
	for _, ref := range names {
		if ref == notifyNone {
			continue
		}
		if _, ok := defined[ref]; !ok {
			add("%s references unknown notifier %q", prefix, ref)
		}
	}
}

// validateNotifyRefs checks every `then.notify` selection in a watch (entry-level
// and per-metric) against the defined notifiers and the `none` sentinel.
func validateNotifyRefs(name string, entry map[string]any, notifiers map[string]struct{}, add func(string, ...any)) {
	check := func(prefix string, then any) {
		t, ok := then.(map[string]any)
		if !ok {
			return
		}
		if _, present := t["notify"]; present {
			validateNotifySelection(prefix+".then.notify", cfgval.StringList(t["notify"]), notifiers, add)
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
