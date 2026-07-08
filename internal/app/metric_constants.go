package app

const (
	daemonMetricCheck    = "sermod"
	runtimeMetricCheck   = "runtime"
	displayListSeparator = ", "
)

const (
	observabilityMissingStartup = "startup observation"
	observabilityMissingHistory = "availability history"
	observabilityMissingRuntime = "runtime metrics"
)

const (
	watchConditionDefaultMinimum = "1"
	watchConditionDefaultDelta   = "0"
	watchDefaultLockName         = "(default)"
	watchFallbackFilesystem      = "filesystem"
	watchFirewallDefaultMinRules = uint64(1)
	watchMissingDeviceMessage    = "missing device"
	watchMissingInterfaceMessage = "missing interface"
	watchMissingNameMessage      = "missing name"
	watchMissingPathMessage      = "missing path"
)
