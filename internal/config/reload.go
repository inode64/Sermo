package config

import (
	"errors"
	"fmt"
	"strings"
	"syscall"

	"sermo/internal/cfgval"
	"sermo/internal/process"
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
