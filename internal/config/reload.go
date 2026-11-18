package config

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/process"
	"sermo/internal/rules"
)

// ReloadSpec is the parsed native service reload declaration. A zero value
// means no native reload is configured and the init backend owns reload.
type ReloadSpec struct {
	Command    []string
	Signal     syscall.Signal
	HasSignal  bool
	Always     bool
	Configured bool
}

// ParseReload reads and validates the optional native reload declaration. Both
// config validation and operation construction use this parser so a resolved
// tree cannot acquire different reload semantics at runtime.
func ParseReload(tree map[string]any) (ReloadSpec, error) {
	raw, present := tree[SectionReload]
	if !present {
		return ReloadSpec{}, nil
	}
	r, ok := raw.(map[string]any)
	if !ok {
		return ReloadSpec{}, errors.New("reload must be a mapping with a signal or command")
	}

	if value, exists := r[reloadOnUpgrade]; exists {
		if _, ok := value.(bool); !ok {
			return ReloadSpec{}, errors.New("reload.on_upgrade must be a boolean")
		}
		if ReloadOnUpgrade(tree) && (!hasStrictPrincipal(tree) || hasDelegatedSelector(tree)) {
			return ReloadSpec{}, errors.New("reload.on_upgrade requires a strict principal and no delegated selectors")
		}
	}

	when := cfgval.AsString(r[ReloadKeyWhen])
	if when != "" && when != ReloadWhenAuto && when != ReloadWhenAlways {
		return ReloadSpec{}, fmt.Errorf("%s %q must be %s", reloadPathWhen, when, ReloadWhenSummary)
	}

	signalName := cfgval.AsString(r[ReloadKeySignal])
	commandValue, hasCommand := r[ReloadKeyCommand]
	if signalName != "" && hasCommand {
		return ReloadSpec{}, errors.New("reload sets both signal and command; use exactly one")
	}
	spec := ReloadSpec{Always: when == ReloadWhenAlways, Configured: true}
	switch {
	case signalName != "":
		signal, err := process.ParseSignal(signalName)
		if err != nil {
			return ReloadSpec{}, fmt.Errorf("%s %q is not a known signal name (%s)", reloadPathSignal, signalName, strings.Join(process.SignalNames(), ", "))
		}
		spec.Signal = signal
		spec.HasSignal = true
		return spec, nil
	case hasCommand:
		if !cfgval.IsNonEmptyStringArray(commandValue) {
			return ReloadSpec{}, fmt.Errorf("%s must be a non-empty argv array, not a shell string", reloadPathCommand)
		}
		spec.Command = cfgval.StringArray(commandValue)
		return spec, nil
	default:
		return ReloadSpec{}, errors.New("reload must set either signal or command")
	}
}

const reloadOnUpgrade = "on_upgrade"

// ReloadOnUpgrade declares that this service re-executes its installed program on reload.
func ReloadOnUpgrade(tree map[string]any) bool {
	enabled, _ := SectionMap(tree, SectionReload)[reloadOnUpgrade].(bool)
	return enabled
}

// resolveUpgradeActions applies the service policy after both generated and operator
// rules exist. Only a direct package-change condition qualifies; recovery of a
// failed service and explicit manual operations retain their own action.
func resolveUpgradeActions(tree map[string]any) {
	if !ReloadOnUpgrade(tree) {
		return
	}
	for _, raw := range SectionMap(tree, rules.SectionRules) {
		rule, _ := raw.(map[string]any)
		if rule[rules.RuleFieldType] != string(rules.RuleRemediation) || !upgradeCondition(SectionMap(rule, rules.RuleFieldIf), SectionMap(tree, SectionChecks)) {
			continue
		}
		then := SectionMap(rule, rules.RuleFieldThen)
		if then[rules.RuleFieldAction] == string(rules.ActionRestart) {
			then[rules.RuleFieldAction] = string(rules.ActionReload)
		}
		actions, _ := then[rules.RuleFieldActions].([]any)
		for _, entry := range actions {
			action, _ := entry.(map[string]any)
			if action[rules.RuleFieldType] == string(rules.ActionRestart) {
				action[rules.RuleFieldType] = string(rules.ActionReload)
			}
		}
	}
}

func upgradeCondition(condition, probes map[string]any) bool {
	if len(condition) != 1 {
		return false
	}
	changed := SectionMap(condition, rules.ConditionChanged)
	if cfgval.String(changed[rules.FieldApp]) != "" || cfgval.String(changed[rules.FieldLibrary]) != "" {
		return true
	}
	failed := SectionMap(condition, rules.ConditionFailed)
	probe := SectionMap(probes, cfgval.String(failed[rules.FieldCheck]))
	switch probe[checks.CheckKeyType] {
	case checks.CheckTypeStaleBinary, checks.CheckTypeStaleLibraries:
		return true
	default:
		return false
	}
}
