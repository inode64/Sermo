package app

const (
	sermoEnvPrefix = "SERMO_"

	sermoEnvService = sermoEnvPrefix + "SERVICE"
	sermoEnvRule    = sermoEnvPrefix + "RULE"

	sermoEnvWatch     = sermoEnvPrefix + "WATCH"
	sermoEnvCheckType = sermoEnvPrefix + "CHECK_TYPE"
	sermoEnvMessage   = sermoEnvPrefix + "MESSAGE"
	sermoEnvPath      = sermoEnvPrefix + "PATH"
	sermoEnvChange    = sermoEnvPrefix + "CHANGE"
	sermoEnvOld       = sermoEnvPrefix + "OLD"
	sermoEnvNew       = sermoEnvPrefix + "NEW"
	sermoEnvSize      = sermoEnvPrefix + "SIZE"
	sermoEnvOp        = sermoEnvPrefix + "OP"
	sermoEnvValue     = sermoEnvPrefix + "VALUE"

	sermoEnvPID        = sermoEnvPrefix + "PID"
	sermoEnvProcess    = sermoEnvPrefix + "PROCESS"
	sermoEnvAgeSeconds = sermoEnvPrefix + "AGE_SECONDS"
	sermoEnvMemory     = sermoEnvPrefix + "MEMORY"
	sermoEnvUser       = sermoEnvPrefix + "USER"
	sermoEnvCPU        = sermoEnvPrefix + "CPU"
	sermoEnvIO         = sermoEnvPrefix + "IO"

	envFormatBase         = 10
	envFloatBits          = 64
	envFloatFormat        = 'f'
	envFloatPrecisionAuto = -1
	procWatchCPUPrecision = 2
	procWatchIOPrecision  = 0
	fileModeFormat        = "%04o"
	fileOwnerFormat       = "%d:%d"
)
