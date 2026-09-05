package config

import (
	"maps"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/notify"
	"sermo/internal/rules"
	"sermo/internal/telegrambot"
)

const (
	retiredWebKeyPassword      = "password"
	retiredWebKeyGuestPassword = "guest_password"
)

// validateWatches checks each host-watch entry: a known check type with valid
// thresholds and a local action or inherited global notify default.
// validateWeb checks the global `web` block. The UI is enabled only when `port`
// is set to a valid TCP port; a `web` block without `port` (or with port omitted)
// is valid and leaves the dashboard disabled, matching sermod.
func validateWeb(webCfg map[string]any, add func(string, ...any)) {
	if portRaw, present := webCfg[WebKeyPort]; present {
		port, ok := cfgval.Int(portRaw)
		if !ok || !cfgval.ValidTCPPort(port) {
			add(validationTCPPortRangeFormat, webPathPort, cfgval.TCPPortRange())
		}
	}
	if v, present := webCfg[WebKeyAddress]; present {
		if _, isStr := v.(string); !isStr {
			add("%s must be a string", webPathAddress)
		}
	}
	for _, pathAndKey := range [][2]string{
		{webPathPasswordFile, WebKeyPasswordFile},
		{webPathGuestPasswordFile, WebKeyGuestPasswordFile},
	} {
		path, key := pathAndKey[0], pathAndKey[1]
		if v, present := webCfg[key]; present {
			if _, isStr := v.(string); !isStr {
				add("%s must be a string", path)
			}
		}
	}
	validateWebCredentialFiles(webCfg, add)
	for _, fields := range [][2]string{
		{retiredWebKeyPassword, webPathPasswordFile},
		{retiredWebKeyGuestPassword, webPathGuestPasswordFile},
	} {
		retired, replacement := fields[0], fields[1]
		if _, present := webCfg[retired]; present {
			add("%s.%s is no longer supported; use %s with hashed credentials", SectionWeb, retired, replacement)
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

// validateWebCredentialFiles requires a non-empty path for every configured
// hashed-credential source. Loading and parsing happen in resolveWebCredentials.
func validateWebCredentialFiles(webCfg map[string]any, add addFunc) {
	for _, pair := range [][2]string{
		{WebKeyPasswordFile, webPathPasswordFile},
		{WebKeyGuestPasswordFile, webPathGuestPasswordFile},
	} {
		fileKey, filePath := pair[0], pair[1]
		raw, present := webCfg[fileKey]
		if !present {
			continue
		}
		if s, isStr := raw.(string); isStr && strings.TrimSpace(s) == "" {
			add("%s must name a file holding hashed credentials", filePath)
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
	if cfgval.Disabled(entry) {
		return
	}
	validateNotifierTemplate(name, entry, templateDir, add)
	for _, issue := range notify.ValidateEntry(entry) {
		add("%s%s", notifierFieldPath(name, issue.Field), issue.Suffix)
	}
}

// validateTelegramBot checks the optional top-level `telegram_bot` section. The
// token is usually sourced from ${env:...}, so an empty token (an unset
// variable) leaves the bot inactive rather than failing config load — mirroring
// telegrambot.Config.active(). When a token is present the section requires at
// least one allowed chat id; a poll interval, when set, must be positive.
func validateTelegramBot(raw map[string]any, add func(string, ...any)) {
	section, ok := raw[SectionTelegramBot].(map[string]any)
	if !ok {
		return
	}
	field := func(key string) string { return SectionTelegramBot + "." + key }
	if v, present := section[telegrambot.KeyEnabled]; present {
		if _, isBool := v.(bool); !isBool {
			add(validationBooleanFormat, field(telegrambot.KeyEnabled))
		}
	}
	if cfgval.Disabled(section) {
		return
	}
	// An empty token (typically an unset ${env:...} secret) leaves the bot
	// inactive instead of failing validation, so a host without the token still
	// loads its config. Mirrors telegrambot.Config.active().
	if cfgval.String(section[telegrambot.KeyToken]) == "" {
		return
	}
	if ids, ok := cfgval.IntList(section[telegrambot.KeyAllowedChats]); !ok || len(ids) == 0 {
		add("%s must list at least one chat id", field(telegrambot.KeyAllowedChats))
	}
	if v, present := section[telegrambot.KeyPollInterval]; present && cfgval.Duration(v) <= 0 {
		add("%s must be a positive duration", field(telegrambot.KeyPollInterval))
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
