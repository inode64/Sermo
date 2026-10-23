package config

import (
	"sermo/internal/cfgval"
	"sermo/internal/process"
)

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

// ServiceLifecycle is the canonical operational interpretation of a resolved
// service tree for one backend. Runtime-only facts such as the concrete unit
// PID remain outside this immutable configuration model.
type ServiceLifecycle struct {
	ProcessMode    ServiceProcessMode
	AuxiliaryUnits []string
}

// ResolveServiceLifecycle derives the operational lifecycle contract for a
// resolved service tree and active backend; structural validation remains owned
// by the config validator.
func ResolveServiceLifecycle(tree map[string]any, backend string) ServiceLifecycle {
	return ServiceLifecycle{
		ProcessMode:    resolvedProcessMode(tree),
		AuxiliaryUnits: AdditionalUnits(tree, backend),
	}
}

func resolvedProcessMode(tree map[string]any) ServiceProcessMode {
	if hasConfiguredProcessIdentity(tree) {
		return ServiceProcessResident
	}
	if _, explicit := tree[process.SectionProcesses]; explicit {
		return ServiceProcessNone
	}
	return ServiceProcessInit
}

func hasConfiguredProcessIdentity(tree map[string]any) bool {
	if len(cfgval.StringList(tree[process.ServiceKeyPidfile])) > 0 {
		return true
	}
	if pidfiles, ok := tree[process.ServiceKeyPidfiles].(map[string]any); ok && len(pidfiles) > 0 {
		return true
	}
	processes, ok := tree[process.SectionProcesses].(map[string]any)
	return ok && len(processes) > 0
}
