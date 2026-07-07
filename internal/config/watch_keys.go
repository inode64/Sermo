package config

import (
	"sermo/internal/checks"
	"sermo/internal/rules"
)

// WatchKeyCheck is the watch entry key containing its inline check block.
const WatchKeyCheck = "check"

// WatchKeyThen is the watch entry key containing actions for a firing condition.
const WatchKeyThen = rules.RuleFieldThen

// Watch then-block keys shared by validation, builders and web projections.
const (
	WatchThenKeyHook           = "hook"
	WatchThenKeyExpand         = "expand"
	WatchThenKeyKill           = "kill"
	WatchThenKeyNotifyInterval = "notify_interval"
)

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
