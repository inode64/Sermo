package config

import (
	"fmt"
	"maps"
	"slices"

	"sermo/internal/cfgval"
)

// RestartMode selects how a service restart is executed.
type RestartMode string

// Restart policy field keys and modes.
const (
	// RestartPolicyKeyMode is restart_policy.mode.
	RestartPolicyKeyMode = "mode"
	// RestartModeStaged composes a restart from Sermo's stop and start paths.
	RestartModeStaged RestartMode = "staged"
	// RestartModeNative delegates the restart atomically to the init backend.
	RestartModeNative RestartMode = "native"

	restartModeSummary = string(RestartModeStaged) + ", " + string(RestartModeNative)
)

// ParseRestartMode returns the service's restart strategy and rejects malformed
// or unsafe explicit declarations. Schema validation and operation construction
// share this parser so both paths fail closed on the same rules.
func ParseRestartMode(tree map[string]any) (RestartMode, error) {
	raw, present := tree[ServiceKeyRestartPolicy]
	if !present {
		return RestartModeStaged, nil
	}
	policy, ok := raw.(map[string]any)
	if !ok {
		return "", fmt.Errorf(validationMappingFormat, ServiceKeyRestartPolicy)
	}
	for _, key := range slices.Sorted(maps.Keys(policy)) {
		if key != RestartPolicyKeyMode {
			return "", fmt.Errorf(validationNotSupportedFormat, ServiceKeyRestartPolicy+"."+key)
		}
	}
	mode := RestartMode(cfgval.AsString(policy[RestartPolicyKeyMode]))
	if mode != RestartModeStaged && mode != RestartModeNative {
		return "", fmt.Errorf(validationNotOneOfFormat, restartPolicyPathMode, mode, restartModeSummary)
	}
	if mode == RestartModeNative {
		if _, present := tree[SectionControl]; present {
			return "", fmt.Errorf("%s=%q is not supported with %s", restartPolicyPathMode, mode, SectionControl)
		}
	}
	return mode, nil
}
