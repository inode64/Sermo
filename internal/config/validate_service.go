package config

import (
	"maps"
	"slices"
	"strings"
	"time"

	"sermo/internal/cfgval"
)

var validMonitorModes = set(MonitorEnabled, MonitorDisabled, MonitorPrevious)

// validateServiceMonitors validates the per-service `version:`/`config:` monitor
// blocks: their `on_change.notify` selection must reference defined notifiers (or
// the `none` sentinel). The version/config commands themselves are reused from
// the profile (commands.version / preflight.config) and validated there.
func validateServiceMonitors(tree map[string]any, notifiers map[string]struct{}, add addFunc) {
	for _, key := range []string{"version", "config"} {
		block, ok := tree[key].(map[string]any)
		if !ok {
			continue
		}
		oc, present := block["on_change"]
		if !present {
			continue
		}
		ocMap, ok := oc.(map[string]any)
		if !ok {
			add("%s.on_change must be a mapping", key)
			continue
		}
		if _, present := ocMap["notify"]; present {
			validateNotifySelection(key+".on_change.notify", cfgval.StringList(ocMap["notify"]), notifiers, add)
		}
	}
}

func validateStopPolicy(tree map[string]any, add addFunc) {
	sp, ok := tree["stop_policy"].(map[string]any)
	if !ok {
		return
	}
	for _, field := range []string{"graceful_timeout", "term_timeout", "kill_timeout"} {
		if v, present := sp[field]; present && !isPositiveDuration(cfgval.String(v)) {
			add("stop_policy.%s %q must be a valid positive duration", field, cfgval.String(v))
		}
	}
	force, _ := sp["force_kill"].(bool)
	koi, hasKoi := sp["kill_only_if"].(map[string]any)
	if force && !hasKoi {
		add("stop_policy.force_kill=true requires kill_only_if")
	}
	if hasKoi {
		if len(cfgval.StringList(koi["users"])) == 0 || len(cfgval.StringList(koi["exe_any"])) == 0 {
			add("stop_policy.kill_only_if must define both users and exe_any, each non-empty")
		}
	}
}

func validateProcesses(tree map[string]any, add addFunc) {
	processes, ok := tree["processes"].(map[string]any)
	if !ok {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(processes)) {
		path := "processes." + name
		entry, ok := processes[name].(map[string]any)
		if !ok {
			add("%s must be a mapping", path)
			continue
		}
		switch typ := cfgval.String(entry["type"]); typ {
		case "pidfile":
			if len(cfgval.StringList(entry["path"])) == 0 {
				add("%s.path is required for a pidfile selector", path)
			}
		case "command_match":
			if cfgval.String(entry["exe"]) == "" || cfgval.String(entry["user"]) == "" {
				add("%s command_match requires both exe and user", path)
			}
		case "":
			add("%s.type is required", path)
		default:
			add("%s.type %q is not one of pidfile, command_match", path, typ)
		}
	}
}

func validatePolicyExtras(tree map[string]any, add addFunc) {
	policy, ok := tree["policy"].(map[string]any)
	if !ok {
		return
	}
	if v, present := policy["max_actions"]; present {
		if n, ok := cfgval.Int(v); !ok || n <= 0 {
			add("policy.max_actions must be an integer > 0")
		}
		if _, hasWindow := policy["max_actions_window"]; !hasWindow {
			add("policy.max_actions requires policy.max_actions_window")
		}
	}
	if v, present := policy["max_actions_window"]; present && !isPositiveDuration(cfgval.String(v)) {
		add("policy.max_actions_window %q must be a valid positive duration", cfgval.String(v))
	}
	if bo, ok := policy["backoff"].(map[string]any); ok {
		initial := cfgval.String(bo["initial"])
		if !isPositiveDuration(initial) {
			add("policy.backoff.initial must be a valid positive duration")
		}
		di, _ := time.ParseDuration(initial)
		dm, errMax := time.ParseDuration(cfgval.String(bo["max"]))
		if errMax != nil || dm < di {
			add("policy.backoff.max must be >= initial")
		}
	}
}

// validateCommands checks the optional `commands` section: each entry uses array
// form with an optional valid duration timeout (section 30). The engine never
// runs these; they are informational metadata.
func validateCommands(tree map[string]any, add addFunc) {
	commands, ok := tree["commands"].(map[string]any)
	if !ok {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(commands)) {
		entry, ok := commands[name].(map[string]any)
		if !ok {
			add("commands.%s must be a mapping", name)
			continue
		}
		if !isStringArray(entry["command"]) {
			add("commands.%s command must be an array, not a shell string", name)
		}
		if v, present := entry["timeout"]; present && !isPositiveDuration(cfgval.String(v)) {
			add("commands.%s timeout %q must be a valid positive duration", name, cfgval.String(v))
		}
		if v, present := entry["expect_exit"]; present {
			if _, ok := cfgval.Int(v); !ok {
				add("commands.%s expect_exit must be an integer", name)
			}
		}
		validateOutputExpectation("commands."+name, "expect_stdout", entry["expect_stdout"], add)
		validateOutputExpectation("commands."+name, "expect_stderr", entry["expect_stderr"], add)
	}
}

// validateServiceField checks the `service` field: a scalar unit name, a per-init
// map of systemd/openrc candidate lists, or the legacy { name: ... } shorthand.
func validateServiceField(tree map[string]any, add addFunc) {
	s, present := tree["service"]
	if !present {
		return
	}
	switch v := s.(type) {
	case string:
		if strings.TrimSpace(v) == "" {
			add("service must not be empty")
		}
	case map[string]any:
		hasInit, hasName := false, false
		for _, k := range slices.Sorted(maps.Keys(v)) {
			switch k {
			case "systemd", "openrc":
				hasInit = true
				if len(cfgval.StringList(v[k])) == 0 {
					add("service.%s must be a non-empty list", k)
				}
			case "name":
				hasName = true
				if cfgval.String(v["name"]) == "" {
					add("service.name must not be empty")
				}
			default:
				add("service key %q is not one of systemd, openrc, name", k)
			}
		}
		if hasInit && hasName {
			add("service must not mix name with systemd/openrc")
		}
	default:
		add("service must be a unit name or a per-init map (systemd/openrc)")
	}
}

func policyCooldown(tree map[string]any) (string, bool) {
	policy, ok := tree["policy"].(map[string]any)
	if !ok {
		return "", false
	}
	v, present := policy["cooldown"]
	if !present {
		return "", false
	}
	return cfgval.String(v), true
}
