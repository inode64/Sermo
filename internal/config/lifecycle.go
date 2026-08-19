package config

import (
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/process"
)

const systemdSocketUnitSuffix = ".socket"

// ServiceProcessMode describes where a resolved service obtains its process
// identity. It is derived once from the canonical service tree rather than
// inferred independently by monitoring and operation callers.
type ServiceProcessMode string

const (
	// ServiceProcessInit lets the active init backend provide process identity.
	ServiceProcessInit ServiceProcessMode = "init"
	// ServiceProcessResident declares one or more persistent process identities.
	ServiceProcessResident ServiceProcessMode = "resident"
	// ServiceProcessNone explicitly declares a oneshot, kernel-backed, or other
	// service without a persistent userspace process.
	ServiceProcessNone ServiceProcessMode = "none"
)

// ServiceControlMode identifies whether init or an external controller owns
// the service lifecycle.
type ServiceControlMode string

const (
	// ServiceControlInit delegates lifecycle actions to systemd or OpenRC.
	ServiceControlInit ServiceControlMode = "init"
	// ServiceControlExternal delegates lifecycle actions to control.type.
	ServiceControlExternal ServiceControlMode = "external"
)

// ServiceLifecycle is the canonical operational interpretation of a resolved
// service tree for one backend. Runtime-only facts such as the concrete unit
// PID remain outside this immutable configuration model.
type ServiceLifecycle struct {
	ProcessMode       ServiceProcessMode
	ControlMode       ServiceControlMode
	RestartMode       RestartMode
	AuxiliaryUnits    []string
	AllowDependencies bool
	SocketActivated   bool
	DelegatedRoles    []string
}

// ResolveServiceLifecycle derives the operational lifecycle contract for a
// resolved service tree and active backend. Invalid restart policy is returned
// as an error; structural validation remains owned by the config validator.
func ResolveServiceLifecycle(tree map[string]any, backend string) (ServiceLifecycle, error) {
	restartMode, err := ParseRestartMode(tree)
	if err != nil {
		return ServiceLifecycle{}, err
	}

	lifecycle := ServiceLifecycle{
		ProcessMode:       resolvedProcessMode(tree),
		ControlMode:       ServiceControlInit,
		RestartMode:       restartMode,
		AuxiliaryUnits:    AdditionalUnits(tree, backend),
		AllowDependencies: AllowDependencies(tree),
		DelegatedRoles:    delegatedProcessRoles(tree),
	}
	if _, external := tree[SectionControl]; external {
		lifecycle.ControlMode = ServiceControlExternal
	}
	lifecycle.SocketActivated = serviceUsesSocketActivation(tree, backend, lifecycle.AuxiliaryUnits)
	return lifecycle, nil
}

func resolvedProcessMode(tree map[string]any) ServiceProcessMode {
	if hasConfiguredProcessIdentity(tree) {
		return ServiceProcessResident
	}
	if _, explicit := tree[SectionProcesses]; explicit {
		return ServiceProcessNone
	}
	return ServiceProcessInit
}

func hasConfiguredProcessIdentity(tree map[string]any) bool {
	if len(cfgval.StringList(tree[ServiceKeyPidfile])) > 0 {
		return true
	}
	if pidfiles, ok := tree[ServiceKeyPidfiles].(map[string]any); ok && len(pidfiles) > 0 {
		return true
	}
	processes, ok := tree[SectionProcesses].(map[string]any)
	return ok && len(processes) > 0
}

func delegatedProcessRoles(tree map[string]any) []string {
	processes, ok := tree[SectionProcesses].(map[string]any)
	if !ok {
		return nil
	}
	roles := make([]string, 0, len(processes))
	for role, raw := range processes {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		delegated, _ := entry[process.SelectorKeyDelegated].(bool)
		if delegated {
			roles = append(roles, role)
		}
	}
	slices.Sort(roles)
	return roles
}

func serviceUsesSocketActivation(tree map[string]any, backend string, auxiliaryUnits []string) bool {
	for _, unit := range auxiliaryUnits {
		if strings.HasSuffix(unit, systemdSocketUnitSuffix) {
			return true
		}
	}
	if backend != backendSystemd {
		return false
	}
	candidates, _ := ServiceCandidates(tree, backend, "")
	return len(candidates) > 0 && strings.HasSuffix(candidates[0], systemdSocketUnitSuffix)
}
