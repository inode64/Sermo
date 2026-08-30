package app

const (
	// daemonName is how sermod names itself as the subject of its own log
	// records and of the metric series it records for its own process.
	daemonName           = "sermod"
	daemonMetricCheck    = daemonName
	runtimeMetricCheck   = "runtime"
	displayListSeparator = ", "
)

const (
	observabilityMissingStartup = "startup observation"
	observabilityMissingHistory = "availability history"
	observabilityMissingRuntime = "runtime metrics"
	// observabilityMissingProcesses is the definite counterpart of
	// observabilityMissingRuntime: the daemon completed a cycle and attributed
	// no process at all to an active service, so runtime metrics are not late,
	// they are unavailable until the selectors or the host situation change.
	observabilityMissingProcesses = "service processes"
)

// stateReason* are machine-readable causes behind an operator-facing service
// state. They are
// tokens, not prose: the dashboard owns the wording, so it can be reworded or
// translated without the backend and the frontend having to agree on a
// sentence. The observability_missing list stays a set of indicator names.
const (
	stateReasonConfigurationInvalid  = "configuration_invalid"
	stateReasonStaleBinary           = "stale_binary"
	stateReasonFailedUnitLiveProcess = "failed_unit_live_process"
)

const (
	watchConditionDefaultDelta   = "0"
	watchFailedUnitsDefaultCount = "0"
	watchDefaultLockName         = "(default)"
	watchFallbackFilesystem      = "filesystem"
	watchFirewallDefaultMinRules = uint64(1)
)
