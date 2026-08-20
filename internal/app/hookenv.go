package app

import (
	"strconv"
	"time"

	"sermo/internal/checks"
)

const (
	sermoEnvPrefix = "SERMO_"

	sermoEnvService = sermoEnvPrefix + "SERVICE"
	sermoEnvRule    = sermoEnvPrefix + "RULE"

	sermoEnvWatch      = sermoEnvPrefix + "WATCH"
	sermoEnvCheckType  = sermoEnvPrefix + "CHECK_TYPE"
	sermoEnvMessage    = sermoEnvPrefix + "MESSAGE"
	sermoEnvSeverity   = sermoEnvPrefix + "SEVERITY"
	sermoEnvPath       = sermoEnvPrefix + "PATH"
	sermoEnvChange     = sermoEnvPrefix + "CHANGE"
	sermoEnvOld        = sermoEnvPrefix + "OLD"
	sermoEnvNew        = sermoEnvPrefix + "NEW"
	sermoEnvSize       = sermoEnvPrefix + "SIZE"
	sermoEnvModifiedAt = sermoEnvPrefix + "MODIFIED_AT"
	sermoEnvOp         = sermoEnvPrefix + "OP"
	sermoEnvValue      = sermoEnvPrefix + "VALUE"

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
	fileModeFormat        = checks.FileModeFormat
	fileOwnerFormat       = checks.FileOwnerFormat
)

// envAgeSeconds renders a duration as the whole seconds SERMO_AGE_SECONDS
// carries. Every watch that reports an age emits this variable and addSummaryAge
// parses it back, so the encoding belongs next to the key rather than restated
// once per watch.
func envAgeSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(d.Seconds()), envFormatBase)
}
