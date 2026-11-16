package process

import (
	"regexp"
	"strings"
	"unicode"
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

// MatchedName is the name pattern matched in id's command line: the matched
// text without surrounding blanks or slashes, reduced to its last path element
// ("emerge" for "/usr/bin/python3.13 /usr/lib/python-exec/python3.13/emerge
// -vp"). It is "" when nothing matches or that text is not a plain name of
// letters, digits and "._+-": only the predicate's own match is named, never
// the arguments around it, which may carry secrets, and never characters a
// notifier could read as markup.
func MatchedName(pattern *regexp.Regexp, id Identity) string {
	if pattern == nil || id.CmdlineUnreadable {
		return ""
	}
	line := strings.Join(id.Cmdline, " ")
	loc := pattern.FindStringIndex(line)
	if loc == nil {
		return ""
	}
	name := strings.TrimFunc(line[loc[0]:loc[1]], func(r rune) bool { return unicode.IsSpace(r) || r == '/' })
	name = name[strings.LastIndexByte(name, '/')+1:]
	if strings.IndexFunc(name, notNameRune) >= 0 {
		return ""
	}
	return name
}

func notNameRune(r rune) bool {
	return !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune("._+-", r)
}
