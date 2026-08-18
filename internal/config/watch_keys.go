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

// WatchKeyRAIDControl enables explicit manual reconstruction control for a
// single RAID watch.
const WatchKeyRAIDControl = "raid_control"

// RAID control keys configure a manual pause/resume capability.
const (
	RAIDControlKeyPauseResume = "pause_resume"
)

// Watch then-block keys shared by validation, builders and web projections.
const (
	WatchThenKeyHook           = "hook"
	WatchThenKeyExpand         = "expand"
	WatchThenKeyKill           = "kill"
	WatchThenKeyMakeStep       = "makestep"
	WatchThenKeyNotifyInterval = "notify_interval"
	WatchThenKeyNotifyOn       = "notify_on"
)

// IsAlertOnlyWatchThenKey reports whether key is permitted on a watch that may
// only record and deliver an alert. Keep this vocabulary shared by config
// validation and runtime builders so an unchecked config cannot widen it.
func IsAlertOnlyWatchThenKey(key string) bool {
	return key == rules.RuleFieldNotify || key == WatchThenKeyNotifyInterval
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
