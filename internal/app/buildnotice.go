package app

import (
	"strings"

	"sermo/internal/config"
)

// buildNoticeInfoPrefix marks a build/reload notice as informational rather
// than a warning (e.g. a configured service whose explicit per-init map
// declares no unit for the active backend: the skip is the configuration
// working as written). The prefix is internal transport between the builders
// and LogBuildNotices, which strips it and routes the entry to Info.
const buildNoticeInfoPrefix = "notice: "

// infoNotice marks msg as informational for LogBuildNotices.
func infoNotice(msg string) string { return buildNoticeInfoPrefix + msg }

// buildNoticeLogger is the slog-shaped subset LogBuildNotices needs.
type buildNoticeLogger interface {
	Info(msg string, args ...any)
	Warn(msg string, args ...any)
}

const (
	buildNoticeFieldWarning = "warning"
	buildNoticeFieldNotice  = "notice"
)

// LogBuildNotices logs one builder's notice list under label: entries marked
// with infoNotice go to Info, everything else keeps the historic Warn level.
func LogBuildNotices(logger buildNoticeLogger, label string, notices []string) {
	for _, n := range notices {
		if rest, ok := strings.CutPrefix(n, buildNoticeInfoPrefix); ok {
			logger.Info(label, buildNoticeFieldNotice, rest)
			continue
		}
		logger.Warn(label, buildNoticeFieldWarning, n)
	}
}

// ConfigWarnings renders the advisory findings of an accepted configuration
// (config.Warnings) as notices for LogBuildNotices, each naming its scope.
func ConfigWarnings(cfg *config.Config) []string {
	issues := config.Warnings(cfg)
	notices := make([]string, 0, len(issues))
	for _, issue := range issues {
		notices = append(notices, issue.Scope+": "+issue.Msg)
	}
	return notices
}
