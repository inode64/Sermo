package config

import (
	"fmt"

	"sermo/internal/dockerctl"
	"sermo/internal/rules"
	"sermo/internal/virt"
)

const (
	enginePathBackend               = SectionEngine + "." + EngineKeyBackend
	enginePathDiagnostics           = SectionEngine + "." + EngineKeyDiagnostics
	enginePathDiagnosticsInterval   = SectionEngine + "." + EngineKeyDiagnosticsInterval
	enginePathMaxParallelChecks     = SectionEngine + "." + EngineKeyMaxParallelChecks
	enginePathMaxParallelOperations = SectionEngine + "." + EngineKeyMaxParallelOperations
	enginePathStartupDelay          = SectionEngine + "." + EngineKeyStartupDelay
	enginePathStateCacheSize        = SectionEngine + "." + EngineKeyStateCacheSize
	enginePathUserLookup            = SectionEngine + "." + EngineKeyUserLookup
	enginePathUserLookupTimeout     = SectionEngine + "." + EngineKeyUserLookupTimeout

	pathsPathLocks     = SectionPaths + "." + pathKeyLocks
	pathsPathRuntime   = SectionPaths + "." + pathKeyRuntime
	pathsPathState     = SectionPaths + "." + pathKeyState
	pathsPathTemplates = SectionPaths + "." + pathKeyTemplates

	webPathAddress       = SectionWeb + "." + WebKeyAddress
	webPathGuest         = SectionWeb + "." + WebKeyGuest
	webPathGuestPassword = SectionWeb + "." + WebKeyGuestPassword
	webPathPassword      = SectionWeb + "." + WebKeyPassword
	webPathPort          = SectionWeb + "." + WebKeyPort

	policyPathBackoff          = sectionPolicy + "." + rules.PolicyKeyBackoff
	policyPathBackoffInitial   = policyPathBackoff + "." + rules.BackoffKeyInitial
	policyPathBackoffMax       = policyPathBackoff + "." + rules.BackoffKeyMax
	policyPathCooldown         = sectionPolicy + "." + rules.PolicyKeyCooldown
	policyPathMaxActions       = sectionPolicy + "." + rules.PolicyKeyMaxActions
	policyPathMaxActionsWindow = sectionPolicy + "." + rules.PolicyKeyMaxActionsWindow
	defaultsPathPolicyCooldown = sectionDefaults + "." + policyPathCooldown
	defaultsPathVariables      = sectionDefaults + "." + sectionVariables

	versionsPathCurrentFrom = keyVersions + "." + keyVersionsCurrentFrom
	versionsPathFrom        = keyVersions + "." + keyVersionsFrom

	mountPath              = StorageKeyMount
	mountPathRefcount      = mountPath + "." + MountKeyRefcount
	mountPathStopPolicy    = mountPath + "." + MountKeyStopPolicy
	mountPathStopPolicyKoi = mountPathStopPolicy + "." + keyKillOnlyIf
	mountPathUmount        = mountPath + "." + MountKeyUmount
	mountPathUmountSIGKILL = mountPathUmount + "." + MountKeyAllowSIGKILL

	controlPathContainer = SectionControl + "." + dockerctl.ControlKeyContainer
	controlPathDomain    = SectionControl + "." + virt.ControlKeyDomain
	controlPathHost      = SectionControl + "." + virt.ControlKeyHost
	controlPathPort      = SectionControl + "." + virt.ControlKeyPort
	controlPathSocket    = SectionControl + "." + virt.ControlKeySocket
	controlPathTLS       = SectionControl + "." + dockerctl.ControlKeyTLS
	controlPathType      = SectionControl + "." + keyType
	controlPathURI       = SectionControl + "." + virt.ControlKeyURI
	controlPathUUID      = SectionControl + "." + virt.ControlKeyUUID

	reloadPathCommand = SectionReload + "." + ReloadKeyCommand
	reloadPathSignal  = SectionReload + "." + ReloadKeySignal
	reloadPathWhen    = SectionReload + "." + ReloadKeyWhen

	stopPolicyPathCleanOnStop = sectionStopPolicy + "." + keyCleanOnStop
	stopPolicyPathFilesAbsent = sectionStopPolicy + "." + keyFilesAbsent
	stopPolicyPathForceKill   = sectionStopPolicy + "." + keyForceKill
	stopPolicyPathKillOnlyIf  = sectionStopPolicy + "." + keyKillOnlyIf
)

func engineFieldPath(field string) string {
	return SectionEngine + "." + field
}

func pathsFieldPath(field string) string {
	return SectionPaths + "." + field
}

func defaultsFieldPath(field string) string {
	return sectionDefaults + "." + field
}

func defaultsVariablePath(name string) string {
	return defaultsPathVariables + "." + name
}

func securityFieldPath(field string) string {
	return sectionSecurity + "." + field
}

func mountUmountFieldPath(field string) string {
	return mountPathUmount + "." + field
}

func notifierPath(name string) string {
	return pathKeyNotifiers + "." + name
}

func notifierFieldPath(name, field string) string {
	return notifierPath(name) + "." + field
}

func variablePath(name string) string {
	return SectionVariables + "." + name
}

func variableFieldPath(name, field string) string {
	return variablePath(name) + "." + field
}

func processEntryPath(name string) string {
	return SectionProcesses + "." + name
}

func processFieldPath(name, field string) string {
	return processEntryPath(name) + "." + field
}

func pidfilesRolePath(role string) string {
	return ServiceKeyPidfiles + "." + role
}

func serviceBackendPath(backend string) string {
	return ServiceKeyService + "." + backend
}

func alsoServiceBackendPath(backend string) string {
	return ServiceKeyAlsoService + "." + backend
}

func serviceMonitorFieldPath(monitor, field string) string {
	return monitor + "." + field
}

func serviceMonitorOnChangePath(monitor string) string {
	return serviceMonitorFieldPath(monitor, ServiceMonitorKeyOnChange)
}

func serviceMonitorOnChangeFieldPath(monitor, field string) string {
	return serviceMonitorOnChangePath(monitor) + "." + field
}

func stopPolicyFieldPath(field string) string {
	return sectionStopPolicy + "." + field
}

func stopPolicyCleanOnStopEntryPath(i int) string {
	return fmt.Sprintf("%s[%d]", stopPolicyPathCleanOnStop, i)
}

func watchPath(name string) string {
	return SectionWatches + "." + name
}

func watchFieldPath(name, field string) string {
	return watchPath(name) + "." + field
}

func watchCheckPath(name string) string {
	return watchFieldPath(name, WatchKeyCheck)
}

func watchCheckFieldPath(name, field string) string {
	return watchCheckPath(name) + "." + field
}

func watchMetricsPath(name string) string {
	return watchFieldPath(name, sectionMetrics)
}

func watchMetricPath(name, metric string) string {
	return watchMetricsPath(name) + "." + metric
}

func thenFieldPath(prefix, field string) string {
	return prefix + "." + rules.RuleFieldThen + "." + field
}

func thenHookPath(prefix string) string {
	return thenFieldPath(prefix, WatchThenKeyHook)
}

func thenKillPath(prefix string) string {
	return thenFieldPath(prefix, WatchThenKeyKill)
}
