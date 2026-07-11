package cli

import (
	"sermo/internal/config"
	"sermo/internal/mountctl"
	"sermo/internal/netutil"
	"sermo/internal/servicemgr"
)

const (
	commandHelp      = "help"
	commandVersion   = "version"
	commandBackend   = "backend"
	commandStatus    = "status"
	commandIsActive  = "is-active"
	commandWatch     = "watch"
	commandStart     = actionStart
	commandStop      = actionStop
	commandRestart   = actionRestart
	commandReload    = actionReload
	commandResume    = actionResume
	commandMonitor   = "monitor"
	commandUnmonitor = "unmonitor"
	commandPreflight = "preflight"
	commandProcesses = "processes"
	commandLocks     = "locks"
	commandLock      = "lock"
	commandMount     = mountctl.ActionMount
	commandUmount    = mountctl.ActionUmount
	commandConfig    = "config"
	commandDaemon    = "daemon"
	commandNotifier  = "notifier"
	commandServices  = "services"
	commandApps      = "apps"
	commandLibs      = "libs"
	commandPatterns  = "patterns"
	commandWizard    = "wizard"
	commandEvents    = "events"
	commandActivity  = "activity"
	commandSLA       = "sla"
	commandState     = "state"
	commandPanic     = "panic"
	commandValidate  = "validate"
)

const (
	commandMountList    = "list"
	commandNotifierTest = "test"
	commandStateCompact = "compact"
	commandArgAll       = config.SelectionKeywordAll
	commandArgClear     = "clear"
)

const (
	commandLockAcquire = "acquire"
	commandLockRelease = "release"
)

const (
	commandPanicEnable  = "enable"
	commandPanicDisable = "disable"
	commandPanicOn      = "on"
	commandPanicOff     = "off"
)

const (
	monitorStatusPaused    = "paused"
	monitorStatusResumed   = "resumed"
	monitorStatusNotPaused = "not-paused"
)

const (
	defaultWebAPIAddress = netutil.LoopbackIPv4
	daemonPIDFilename    = config.DaemonPIDFilename
)

const (
	cliFieldSermoService = "SERMO_SERVICE"
	cliFieldSermoAction  = "SERMO_ACTION"
	cliFieldSermoStatus  = "SERMO_STATUS"
	cliDisplayUnknown    = string(servicemgr.StatusUnknown)

	cliFieldSermoReport        = "SERMO_REPORT"
	cliFieldSermoReportHost    = "SERMO_REPORT_HOST"
	cliFieldSermoReportTotal   = "SERMO_REPORT_TOTAL"
	cliFieldSermoReportOK      = "SERMO_REPORT_OK"
	cliFieldSermoReportIssues  = "SERMO_REPORT_ISSUES"
	cliFieldSermoReportMissing = "SERMO_REPORT_MISSING"
)
