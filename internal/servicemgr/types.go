package servicemgr

import (
	"fmt"
	"strings"
)

// Backend identifies a supported service manager backend.
type Backend string

// Supported service-manager backends.
const (
	BackendAuto    Backend = "auto"
	BackendSystemd Backend = "systemd"
	BackendOpenRC  Backend = "openrc"
	BackendLibvirt Backend = "libvirt"
	BackendDocker  Backend = "docker"
	// BackendInitSummary is the user-facing list of selectable init backends.
	BackendInitSummary = string(BackendAuto) + ", " + string(BackendSystemd) + " or " + string(BackendOpenRC)
)

// Init-system command binaries invoked through the execx runner.
const (
	cmdSystemctl = "systemctl"
	cmdRcService = "rc-service"
	cmdRcStatus  = "rc-status"
)

const commandArgTerminator = "--"

// systemctl subcommands, flags and properties used by service-manager probes.
const (
	systemctlCmdCat             = "cat"
	systemctlCmdIsActive        = "is-active"
	systemctlCmdIsSystemRunning = "is-system-running"
	systemctlCmdListUnits       = "list-units"
	systemctlCmdShow            = "show"

	systemctlFlagNoLegend      = "--no-legend"
	systemctlFlagNoPager       = "--no-pager"
	systemctlFlagProperty      = "-p"
	systemctlFlagStateActive   = "--state=active"
	systemctlFlagTypeService   = "--type=service"
	systemctlFlagValue         = "--value"
	systemctlPropertyCanReload = "CanReload"
	systemctlPropertyCGroup    = "ControlGroup"
	systemctlPropertyExecStart = "ExecStart"
	systemctlPropertyMainPID   = "MainPID"
	systemctlPropertyPIDFile   = "PIDFile"
)

// systemd tokens consumed from command output or used to normalize unit names.
const (
	systemdProcessName       = "systemd"
	systemdRuntimeDir        = "/run/systemd/system"
	systemdUnitHeader        = "UNIT"
	systemdServiceSuffix     = ".service"
	systemdSocketSuffix      = ".socket"
	systemdTargetSuffix      = ".target"
	systemdMountSuffix       = ".mount"
	systemdAutomountSuffix   = ".automount"
	systemdSwapSuffix        = ".swap"
	systemdPathSuffix        = ".path"
	systemdTimerSuffix       = ".timer"
	systemdSliceSuffix       = ".slice"
	systemdScopeSuffix       = ".scope"
	systemdDeviceSuffix      = ".device"
	systemdStateRunning      = "running"
	systemdStateDegraded     = "degraded"
	systemdStateDeactivating = "deactivating"
	systemdValueYes          = "yes"
)

const (
	serviceOutputLineSeparator = "\n"
	serviceOutputLineByte      = '\n'

	openRCRuntimeDir = "/run/openrc"
	openRCInitDir    = "/etc/init.d"
	openRCConfDir    = "/etc/conf.d"
	openRCDaemonsDir = "/run/openrc/daemons"
	pid1CommPath     = "/proc/1/comm"
)

// SystemdRuntimeDir is systemd's runtime unit directory.
const SystemdRuntimeDir = systemdRuntimeDir

// OpenRCRuntimeDir is OpenRC's runtime state directory.
const OpenRCRuntimeDir = openRCRuntimeDir

// Service-manager action verbs passed to init backend commands.
const (
	actionStart       = "start"
	actionStop        = "stop"
	actionStatus      = "status"
	actionRestart     = "restart"
	actionReload      = "reload"
	actionResetFailed = "reset-failed"
	actionZap         = "zap"
)

const (
	openRCFlagAll      = "--all"
	openRCFlagAllShort = "-a"
)

// ParseBackend parses a backend name used by CLI flags and environment values.
func ParseBackend(value string) (Backend, error) {
	switch Backend(strings.TrimSpace(strings.ToLower(value))) {
	case "", BackendAuto:
		return BackendAuto, nil
	case BackendSystemd:
		return BackendSystemd, nil
	case BackendOpenRC:
		return BackendOpenRC, nil
	default:
		return "", fmt.Errorf("unknown backend %q (expected %s)", value, BackendInitSummary)
	}
}

// Status is the normalized service status returned by managers.
type Status string

// Normalized service statuses.
const (
	StatusActive   Status = "active"
	StatusInactive Status = "inactive"
	StatusPaused   Status = "paused"
	StatusFailed   Status = "failed"
	StatusUnknown  Status = "unknown"
	// StatusSummary is the user-facing list of normalized service statuses.
	StatusSummary = string(StatusActive) + ", " +
		string(StatusInactive) + ", " +
		string(StatusPaused) + ", " +
		string(StatusFailed) + ", " +
		string(StatusUnknown)
)
