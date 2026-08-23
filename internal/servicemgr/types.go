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
	// BackendLibvirtNetwork controls a libvirt virtual network (net-start /
	// net-destroy), not a domain.
	BackendLibvirtNetwork Backend = "libvirt-network"
	BackendDocker         Backend = "docker"
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

	// systemctlFlagIsolateJob keeps a start/stop job from propagating to other
	// units: it neither pulls in what the unit requires nor drags along the
	// units bound to it (Requires=, BindsTo=, PartOf=). systemd documents this
	// job mode as dangerous precisely because it does that — which is the point
	// here, and why it is opt-out per service rather than unconditional.
	systemctlFlagIsolateJob = "--job-mode=ignore-dependencies"

	systemctlFlagNoLegend      = "--no-legend"
	systemctlFlagNoPager       = "--no-pager"
	systemctlFlagProperty      = "-p"
	systemctlFlagStateActive   = "--state=active"
	systemctlFlagTypeService   = "--type=service"
	systemctlFlagValue         = "--value"
	systemctlPropertyCanReload = "CanReload"
	systemctlPropertyCGroup    = "ControlGroup"
	systemctlPropertyExecStart = "ExecStart"
	systemctlPropertyLoadState = "LoadState"
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
	// systemdLoadStateNotFound is what `systemctl show -p LoadState` reports for
	// a unit systemd cannot find. `is-active` collapses that case into
	// "inactive", so this is the only way to tell a missing unit from a stopped
	// one.
	systemdLoadStateNotFound = "not-found"
	systemdValueYes          = "yes"
)

// The failed-unit listing (ListFailedUnits). `--plain` drops the status bullet
// systemd puts in front of a failed unit, but older versions print it anyway as
// a field of its own, so the parser skips it too.
const (
	systemctlFlagStateFailed = "--state=failed"
	systemctlFlagPlain       = "--plain"
	systemdUnitStatusBullet  = "●"
)

const (
	serviceOutputLineSeparator = "\n"
	serviceOutputLineByte      = '\n'

	openRCRuntimeDir = "/run/openrc"
	openRCInitDir    = "/etc/init.d"
	openRCConfDir    = "/etc/conf.d"
	openRCDaemonsDir = openRCRuntimeDir + "/daemons"
	pid1CommPath     = "/proc/1/comm"
)

// SystemdRuntimeDir is systemd's runtime unit directory.
const SystemdRuntimeDir = systemdRuntimeDir

// SystemdServiceSuffix is the suffix for systemd service units.
const SystemdServiceSuffix = systemdServiceSuffix

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
	// openRCFlagNoDeps is rc-service's `-D, --nodeps`: run the command against
	// this service alone, without starting what it needs or stopping what needs
	// it. rc-service takes its options before the service name.
	openRCFlagNoDeps = "--nodeps"
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
