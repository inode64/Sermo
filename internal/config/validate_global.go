package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/notify"
)

// validateWatches checks each host-watch entry: a known check type with valid
// thresholds and a local action or inherited global notify default.
// validateWeb checks the global `web` block. The UI is enabled only when `port`
// is set to an integer in 1..65535; a `web` block without `port` (or with port
// omitted) is valid and leaves the dashboard disabled, matching sermod.
func validateWeb(webCfg map[string]any, add func(string, ...any)) {
	if portRaw, present := webCfg[WebKeyPort]; present {
		port, ok := cfgval.Int(portRaw)
		if !ok || port < 1 || port > 65535 {
			add("web.port must be an integer in 1..65535")
		}
	}
	if v, present := webCfg[WebKeyAddress]; present {
		if _, isStr := v.(string); !isStr {
			add("web.address must be a string")
		}
	}
	for _, key := range []string{WebKeyPassword, WebKeyGuestPassword} {
		if v, present := webCfg[key]; present {
			if _, isStr := v.(string); !isStr {
				add("web.%s must be a string", key)
			}
		}
	}
	if v, present := webCfg[WebKeyGuest]; present {
		if _, isBool := v.(bool); !isBool {
			add("web.guest must be a boolean (allow anonymous read-only access)")
		}
	}
}

// NotifyNone is the reserved notify sentinel: a notify selection of `none`
// suppresses delivery, and no notifier may take it as a name.
const NotifyNone = "none"

// validateNotifiers checks the global `notifiers` section: each entry is a known
// type with the fields that type needs. New transports validate here too.
func validateNotifiers(notifiers map[string]any, templateDir string, add func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(notifiers)) {
		if name == NotifyNone {
			add("notifiers.%s: %q is a reserved keyword and cannot name a notifier", name, NotifyNone)
			continue
		}
		entry, ok := notifiers[name].(map[string]any)
		if !ok {
			add("notifiers.%s must be a mapping", name)
			continue
		}
		if v, present := entry[keyEnabled]; present {
			if _, ok := v.(bool); !ok {
				add("notifiers.%s.enabled must be a boolean", name)
			}
		}
		if enabled, ok := entry[keyEnabled].(bool); ok && !enabled {
			continue
		}
		validateNotifierTemplate(name, entry, templateDir, add)
		switch typ := cfgval.String(entry["type"]); typ {
		case notifierTypeEmail:
			dsn := cfgval.String(entry["dsn"])
			if dsn == "" {
				add("notifiers.%s.dsn is required for an email notifier", name)
			} else if !strings.HasPrefix(dsn, "smtp://") && !strings.HasPrefix(dsn, "smtps://") {
				add("notifiers.%s.dsn must be an smtp:// or smtps:// URL", name)
			}
			if cfgval.String(entry["from"]) == "" {
				add("notifiers.%s.from is required for an email notifier", name)
			}
			if !cfgval.IsNonEmptyStringList(entry["to"]) {
				add("notifiers.%s.to must list at least one address", name)
			}
		case notifierTypeSlack, notifierTypeTeams:
			wh := cfgval.String(entry["webhook"])
			if wh == "" {
				add("notifiers.%s.webhook is required for a %s notifier", name, typ)
			} else if !strings.HasPrefix(wh, "http://") && !strings.HasPrefix(wh, "https://") {
				add("notifiers.%s.webhook must be an http(s) URL", name)
			}
		case notifierTypeTTY:
			if users, present := entry["users"]; present && !cfgval.IsStringOrStringList(users) {
				add("notifiers.%s.users must be a string or list of strings", name)
			}
		case notifierTypeWall:
			if _, present := entry["users"]; present {
				add("notifiers.%s.users is not supported for a wall notifier; use type tty to target specific users", name)
			}
		case "":
			add("notifiers.%s.type is required", name)
		default:
			// The vocabulary comes from the notify registry, so adding a
			// transport there cannot leave validation rejecting it by drift.
			if !slices.Contains(notify.SupportedTypes(), typ) {
				add("notifiers.%s.type %q is not supported (%s)", name, typ, strings.Join(notify.SupportedTypes(), ", "))
			}
		}
	}
}

func validateNotifierTemplate(name string, entry map[string]any, templateDir string, add func(string, ...any)) {
	raw, present := entry["template"]
	if !present {
		return
	}
	templateName, ok := raw.(string)
	if !ok || strings.TrimSpace(templateName) == "" {
		add("notifiers.%s.template must be a template name", name)
		return
	}
	if _, err := notify.LoadTemplate(templateDir, templateName); err != nil {
		add("notifiers.%s.template %q is invalid: %v", name, templateName, err)
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
	names := cfgval.StringList(raw[sectionNotify])
	if slices.Contains(names, NotifyNone) {
		return nil
	}
	return names
}

// validateNotifySelection validates a notify selection (a global `notify`, a
// watch `then.notify`, or a rule `notify`): it must be a string or string list,
// every name must be a defined notifier or the `none` sentinel, and `none`
// cannot be combined with real names.
func validateNotifySelection(prefix string, raw any, defined map[string]struct{}, add func(string, ...any)) {
	names, err := cfgval.StrictStringList(raw)
	if err != nil {
		add("%s must be a string or list of strings", prefix)
		return
	}
	if slices.Contains(names, NotifyNone) && len(names) > 1 {
		add("%s: %q cannot be combined with notifier names", prefix, NotifyNone)
	}
	for _, ref := range names {
		if ref == NotifyNone {
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
			validateNotifySelection(prefix+".then.notify", t["notify"], notifiers, add)
		}
	}
	check("watches."+name, entry["then"])
	if metrics, ok := entry[sectionMetrics].(map[string]any); ok {
		for _, key := range slices.Sorted(maps.Keys(metrics)) {
			if m, ok := metrics[key].(map[string]any); ok {
				check(fmt.Sprintf("watches.%s.metrics.%s", name, key), m["then"])
			}
		}
	}
}

// reservedVarNames cannot be used as custom variable names in defaults.variables:
// the selection keywords (all/none/default) and the runtime-only tokens
// (date/event/action). Builtins (host/port/…) are intentionally NOT reserved —
// a custom variable may override them. Duplicate names are already rejected by
// the YAML parser (a mapping key defined twice is a load error).
var reservedVarNames = set("all", "none", "default", "date", "event", "action")

// validateDefaultsVariables checks the optional defaults.variables map: it must be
// a mapping; each value must be a scalar or a list (not a nested mapping); and no
// name may be reserved.
func validateDefaultsVariables(defaults map[string]any, add addFunc) {
	v, present := defaults[sectionVariables]
	if !present {
		return
	}
	m, ok := v.(map[string]any)
	if !ok {
		add("defaults.variables must be a mapping of name -> value")
		return
	}
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if _, reserved := reservedVarNames[name]; reserved {
			add("defaults.variables: %q is a reserved name and cannot be a custom variable", name)
		}
		if _, isMap := m[name].(map[string]any); isMap {
			add("defaults.variables.%s must be a scalar or a list, not a mapping", name)
		}
	}
}

func defaultsCooldown(defaults map[string]any) (string, bool) {
	policy, ok := defaults[sectionPolicy].(map[string]any)
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
