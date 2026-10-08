package config

import (
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/rules"
)

// WatchKeyCheck is the watch entry key containing its inline check block.
const WatchKeyCheck = "check"

// WatchKeyThen is the watch entry key containing actions for a firing condition.
const WatchKeyThen = rules.RuleFieldThen

// WatchKeySeverity grades how serious this watch's failures are. It aliases the
// check key so `severity` has one spelling wherever it is declared: on the watch
// entry, on its check block, or on one metric of a multi-metric watch.
const WatchKeySeverity = checks.CheckKeySeverity

// WatchKeyRAIDControl enables explicit manual reconstruction control for a
// single RAID watch.
const WatchKeyRAIDControl = "raid_control"

// RAID control keys configure a manual pause/resume capability.
const (
	RAIDControlKeyPauseResume = "pause_resume"
)

// WatchKeyReplicationControl enables the explicit manual replication start for
// a single replication watch.
const WatchKeyReplicationControl = "replication_control"

// Replication control keys configure the manual start capability.
const (
	ReplicationControlKeyStart = "start"
)

// Watch then-block keys shared by validation, builders and web projections.
const (
	WatchThenKeyHook           = "hook"
	WatchThenKeyRecoverHook    = "recover_hook"
	WatchThenKeyExpand         = "expand"
	WatchThenKeyKill           = "kill"
	WatchThenKeyKillQuery      = "kill_query"
	WatchThenKeyMakeStep       = "makestep"
	WatchThenKeyRemount        = "remount"
	WatchThenKeyNotifyInterval = "notify_interval"
	WatchThenKeyNotifyOn       = "notify_on"
)

// IsAlertOnlyWatchThenKey reports whether key is permitted on a watch that may
// only record and deliver an alert. Keep this vocabulary shared by config
// validation and runtime builders so an unchecked config cannot widen it.
func IsAlertOnlyWatchThenKey(key string) bool {
	return key == rules.RuleFieldNotify || key == WatchThenKeyNotifyInterval
}

// SetWatchActions is the then vocabulary of a watch that evaluates the host
// process table as a set: notification delivery and, for the one type whose
// kill is gated by its own kill_only_if selector, then.kill. Validation and
// the runtime builders share these values so neither can widen the other.
type SetWatchActions struct {
	Type string
	Kill bool
}

// ProcessPolicyActions is the alert-only vocabulary of a process_policy watch.
var ProcessPolicyActions = SetWatchActions{Type: checks.CheckTypeProcessPolicy}

// UnownedProcessesActions is the vocabulary of an unowned_processes watch.
var UnownedProcessesActions = SetWatchActions{Type: checks.CheckTypeUnownedProcesses, Kill: true}

// Accepts reports whether a then key belongs to this vocabulary.
func (a SetWatchActions) Accepts(key string) bool {
	return IsAlertOnlyWatchThenKey(key) || (a.Kill && key == WatchThenKeyKill)
}

// Description names the watch in a rejection, after "not valid on an".
func (a SetWatchActions) Description() string {
	if a.Kill {
		return a.Type + " watch; it accepts " + rules.RuleFieldNotify + ", " + WatchThenKeyNotifyInterval + " and " + WatchThenKeyKill
	}
	return "alert-only " + a.Type + " watch"
}

// EmptyThenMessage explains a present then block that selects nothing.
func (a SetWatchActions) EmptyThenMessage() string {
	if a.Kill {
		return "requires notify and/or kill, or omit then for dashboard/event-log alerts"
	}
	return "requires notify or omit then for dashboard/event-log alerts"
}

// WatchMakeStepKeySocket addresses chronyd's command socket for a clock watch's
// then.makestep action. It aliases the check key so `socket` has one spelling.
const WatchMakeStepKeySocket = checks.CheckKeySocket

// Watch hook keys mirror command-check command/expectation fields.
const (
	WatchHookKeyCommand      = checks.CheckKeyCommand
	WatchHookKeyTimeout      = checks.CheckKeyTimeout
	WatchHookKeyExpectExit   = checks.CheckKeyExpectExit
	WatchHookKeyExpectStdout = checks.CheckKeyExpectStdout
	WatchHookKeyExpectStderr = checks.CheckKeyExpectStderr
)

// Watch kill-action keys configure a process watch's then.kill action.
const (
	WatchKillKeySignal      = "signal"
	WatchKillKeyEscalate    = "escalate"
	WatchKillKeyTermTimeout = keyTermTimeout
	WatchKillKeyKillTimeout = keyKillTimeout
)

// WatchExpandKeyBy configures the amount for a storage watch's then.expand action.
const WatchExpandKeyBy = "by"

// FileWatchConditionSummary is the user-facing list of file-watch conditions.
const FileWatchConditionSummary = checks.CheckKeySize + ", " +
	checks.CheckKeyPermissions + ", " +
	checks.CheckKeyOwner + ", " +
	checks.CheckKeyExistence + ", " +
	checks.CheckKeyOlderThan

// ProcessWatchConditionSummary is the user-facing list of process-watch conditions.
const ProcessWatchConditionSummary = checks.CheckKeyFor + ", " +
	metrics.MetricCPU + ", " +
	metrics.MetricMemory + ", " +
	metrics.MetricIO + ", " +
	checks.CheckKeyGone
