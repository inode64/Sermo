package config

import (
	"maps"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/notify"
	"sermo/internal/rules"
)

// validateWatches checks each host-watch entry: a known check type with valid
// thresholds and a local action or inherited global notify default.
// validateWeb checks the global `web` block. The UI is enabled only when `port`
// is set to a valid TCP port; a `web` block without `port` (or with port omitted)
// is valid and leaves the dashboard disabled, matching sermod.
func validateWeb(webCfg map[string]any, add func(string, ...any)) {
	if portRaw, present := webCfg[WebKeyPort]; present {
		port, ok := cfgval.Int(portRaw)
		if !ok || !validTCPPort(port) {
			add(validationTCPPortRangeFormat, webPathPort, cfgval.TCPPortRange())
		}
	}
	if v, present := webCfg[WebKeyAddress]; present {
		if _, isStr := v.(string); !isStr {
			add("%s must be a string", webPathAddress)
		}
	}
	for _, pathAndKey := range [][2]string{
		{webPathPassword, WebKeyPassword},
		{webPathGuestPassword, WebKeyGuestPassword},
	} {
		path, key := pathAndKey[0], pathAndKey[1]
		if v, present := webCfg[key]; present {
			if _, isStr := v.(string); !isStr {
				add("%s must be a string", path)
			}
		}
	}
	if v, present := webCfg[WebKeyGuest]; present {
		if _, isBool := v.(bool); !isBool {
			add("%s must be a boolean (allow anonymous read-only access)", webPathGuest)
		}
	}
	if v, present := webCfg[WebKeyAllowedHosts]; present {
		if _, err := cfgval.StrictStringList(v); err != nil {
			add("%s must be a hostname or list of hostnames", webPathAllowedHosts)
		}
	}
}

// Selection keywords shared by wizard/config selection flows.
const (
	SelectionKeywordAll     = "all"
	SelectionKeywordNone    = "none"
	SelectionKeywordDefault = "default"
)

// Notify selection keywords.
const (
	NotifyKeywordDefault = SelectionKeywordDefault
	// NotifyNone is the reserved notify sentinel: a notify selection of `none`
	// suppresses delivery, and no notifier may take it as a name.
	NotifyNone = SelectionKeywordNone
)

// validateNotifiers checks the global `notifiers` section: each entry is a known
// type with the fields that type needs. New transports validate here too.
func validateNotifiers(notifiers map[string]any, templateDir string, add func(string, ...any)) {
	for _, name := range slices.Sorted(maps.Keys(notifiers)) {
		validateNotifier(name, notifiers[name], templateDir, add)
	}
}

func validateNotifier(name string, raw any, templateDir string, add func(string, ...any)) {
	if name == NotifyNone {
		add("%s: %q is a reserved keyword and cannot name a notifier", notifierPath(name), NotifyNone)
		return
	}
	entry, ok := raw.(map[string]any)
	if !ok {
		add(validationMappingFormat, notifierPath(name))
		return
	}
	if value, present := entry[keyEnabled]; present {
		if _, ok := value.(bool); !ok {
			add(validationBooleanFormat, notifierFieldPath(name, keyEnabled))
		}
	}
	if enabled, ok := entry[keyEnabled].(bool); ok && !enabled {
		return
	}
	validateNotifierTemplate(name, entry, templateDir, add)
	validateNotifierType(name, entry, add)
}

func validateNotifierType(name string, entry map[string]any, add func(string, ...any)) {
	typ := cfgval.String(entry[notify.KeyType])
	switch typ {
	case notify.TypeEmail:
		validateEmailNotifier(name, entry, add)
	case notify.TypeSlack, notify.TypeTeams:
		validateWebhookNotifier(name, typ, entry, add)
	case notify.TypeTTY:
		if users, present := entry[notify.KeyUsers]; present && !cfgval.IsStringOrStringList(users) {
			add(validationStringListFormat, notifierFieldPath(name, notify.KeyUsers))
		}
	case notify.TypeWall:
		if _, present := entry[notify.KeyUsers]; present {
			add("%s is not supported for a wall notifier; use type tty to target specific users", notifierFieldPath(name, notify.KeyUsers))
		}
	case "":
		add(validationRequiredFormat, notifierFieldPath(name, notify.KeyType))
	default:
		if !slices.Contains(notify.SupportedTypes(), typ) {
			add("%s %q is not supported (%s)", notifierFieldPath(name, notify.KeyType), typ, strings.Join(notify.SupportedTypes(), ", "))
		}
	}
}

func validateEmailNotifier(name string, entry map[string]any, add func(string, ...any)) {
	dsn := cfgval.String(entry[notify.KeyDSN])
	if dsn == "" {
		add("%s is required for an email notifier", notifierFieldPath(name, notify.KeyDSN))
	} else if !strings.HasPrefix(dsn, notify.EmailDSNPrefixSMTP) && !strings.HasPrefix(dsn, notify.EmailDSNPrefixSMTPS) {
		add("%s must be an smtp:// or smtps:// URL", notifierFieldPath(name, notify.KeyDSN))
	}
	if cfgval.String(entry[notify.KeyFrom]) == "" {
		add("%s is required for an email notifier", notifierFieldPath(name, notify.KeyFrom))
	}
	if !cfgval.IsNonEmptyStringList(entry[notify.KeyTo]) {
		add("%s must list at least one address", notifierFieldPath(name, notify.KeyTo))
	}
}

func validateWebhookNotifier(name, typ string, entry map[string]any, add func(string, ...any)) {
	webhook := cfgval.String(entry[notify.KeyWebhook])
	if webhook == "" {
		add("%s is required for a %s notifier", notifierFieldPath(name, notify.KeyWebhook), typ)
	} else if !strings.HasPrefix(webhook, notify.WebhookURLPrefixHTTP) && !strings.HasPrefix(webhook, notify.WebhookURLPrefixHTTPS) {
		add("%s must be an http(s) URL", notifierFieldPath(name, notify.KeyWebhook))
	}
}

func validateNotifierTemplate(name string, entry map[string]any, templateDir string, add func(string, ...any)) {
	raw, present := entry[notify.KeyTemplate]
	if !present {
		return
	}
	templateName, ok := raw.(string)
	if !ok || strings.TrimSpace(templateName) == "" {
		add("%s must be a template name", notifierFieldPath(name, notify.KeyTemplate))
		return
	}
	if _, err := notify.LoadTemplate(templateDir, templateName); err != nil {
		add("%s %q is invalid: %v", notifierFieldPath(name, notify.KeyTemplate), templateName, err)
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
		add(validationStringListFormat, prefix)
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
		if _, present := t[rules.RuleFieldNotify]; present {
			validateNotifySelection(thenFieldPath(prefix, rules.RuleFieldNotify), t[rules.RuleFieldNotify], notifiers, add)
		}
	}
	check(watchPath(name), entry[rules.RuleFieldThen])
	if metrics, ok := entry[sectionMetrics].(map[string]any); ok {
		for _, key := range slices.Sorted(maps.Keys(metrics)) {
			if m, ok := metrics[key].(map[string]any); ok {
				check(watchMetricPath(name, key), m[rules.RuleFieldThen])
			}
		}
	}
}

// reservedVarNames cannot be used as custom variable names in defaults.variables:
// the selection keywords (all/none/default) and the runtime-only tokens
// (date/event/action). Builtins (host/port/…) are intentionally NOT reserved —
// a custom variable may override them. Duplicate names are already rejected by
// the YAML parser (a mapping key defined twice is a load error).
var reservedVarNames = set(SelectionKeywordAll, SelectionKeywordNone, SelectionKeywordDefault, runtimeVarDate, runtimeVarEvent, runtimeVarAction)

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
		add("%s must be a mapping of name -> value", defaultsPathVariables)
		return
	}
	for _, name := range slices.Sorted(maps.Keys(m)) {
		if _, reserved := reservedVarNames[name]; reserved {
			add("%s: %q is a reserved name and cannot be a custom variable", defaultsPathVariables, name)
		}
		if _, isMap := m[name].(map[string]any); isMap {
			add("%s must be a scalar or a list, not a mapping", defaultsVariablePath(name))
		}
	}
}

func isValidBackend(b string) bool {
	_, ok := validBackends[b]
	return ok
}
