package process

import (
	"regexp"
	"strings"
)

// MatchCommand applies a read-only argv predicate. A failed read cannot prove
// absence; a successful empty read can. It never supplies signal authority.
func MatchCommand(pattern *regexp.Regexp, id Identity) (matched, known bool) {
	if pattern == nil {
		return true, true
	}
	if id.CmdlineUnreadable {
		return false, false
	}
	return pattern.MatchString(strings.Join(id.Cmdline, " ")), true
}
