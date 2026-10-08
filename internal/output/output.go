// Package output formats captured stdout, stderr and probe text for stable
// comparisons and compact diagnostics.
package output

import (
	"strings"
	"unicode/utf8"
)

const (
	outputLineBreak          = '\n'
	outputLineSeparator      = "\n"
	streamLabelStdout        = "stdout"
	streamLabelStderr        = "stderr"
	streamLabelSeparator     = ":\n"
	truncatedOutputPrefix    = "… (truncated)\n"
	truncatedFirstLineOffset = 1
)

// FirstNonEmptyLine returns the first non-empty line of s, trimmed.
func FirstNonEmptyLine(s string) string {
	for line := range strings.SplitSeq(s, outputLineSeparator) {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// observedMaxRunes bounds Observed: enough for a few property lines or a
// version banner, short enough to keep an event message on one line.
const (
	observedMaxRunes = 120
	observedEllipsis = "…"
	observedSpace    = " "
)

// Observed renders captured output as the value an assertion saw: whitespace
// runs (including line breaks) collapse to one space and the result is bounded,
// so "LoadState=loaded\nResult=exit-code" reads as one short phrase in a
// mismatch message instead of being hidden behind the expected pattern.
func Observed(s string) string {
	collapsed := strings.Join(strings.Fields(s), observedSpace)
	if utf8.RuneCountInString(collapsed) <= observedMaxRunes {
		return collapsed
	}
	runes := []rune(collapsed)
	return string(runes[:observedMaxRunes]) + observedEllipsis
}

// causeIntroducer ends a line that only introduces the reason on the next one.
const causeIntroducer = ":"

// cause returns the line a failing command states its reason on: the first
// non-empty line, joined with the next non-empty one when it only introduces
// it — Apache's configtest reports "AH00526: Syntax error on line 3 of FILE:"
// and the directive it rejected on the line below.
func cause(s string) string {
	var first string
	for line := range strings.SplitSeq(s, outputLineSeparator) {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			continue
		case first == "":
			if !strings.HasSuffix(t, causeIntroducer) {
				return t
			}
			first = t
		default:
			return first + " " + t
		}
	}
	return first
}

// FailureCause is a failing command's stated reason: Cause of stderr, or of
// stdout for a command that reports errors there.
func FailureCause(stdout, stderr string) string {
	if cause := cause(stderr); cause != "" {
		return cause
	}
	return cause(stdout)
}

// Bounds for Bounded: command output kept in an event is capped so a chatty
// command cannot bloat the event log or the dashboard.
const (
	boundedMaxLines = 40
	boundedMaxBytes = 4096
)

// Bounded combines a failing command's stdout and stderr into the diagnostic
// blob stored on an event's Output field. It keeps the tail, where errors
// usually print, and prefixes a truncation marker when anything was dropped.
func Bounded(stdout, stderr string) string {
	var parts []string
	for _, stream := range []struct {
		label string
		text  string
	}{
		{label: streamLabelStdout, text: stdout},
		{label: streamLabelStderr, text: stderr},
	} {
		if section := streamSection(stream.label, stream.text); section != "" {
			parts = append(parts, section)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return boundTail(strings.Join(parts, outputLineSeparator))
}

func streamSection(label, text string) string {
	if s := strings.TrimSpace(text); s != "" {
		return label + streamLabelSeparator + s
	}
	return ""
}

func boundTail(s string) string {
	truncated := false
	s, cut := tailLines(s, boundedMaxLines)
	truncated = truncated || cut
	s, cut = tailBytes(s, boundedMaxBytes)
	truncated = truncated || cut
	if truncated {
		return truncatedOutputPrefix + s
	}
	return s
}

func tailLines(s string, limit int) (string, bool) {
	lines := strings.Split(s, outputLineSeparator)
	if len(lines) > limit {
		return strings.Join(lines[len(lines)-limit:], outputLineSeparator), true
	}
	return s, false
}

func tailBytes(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	s = s[len(s)-limit:]
	// Drop a partial first line left by the byte cut.
	if i := strings.IndexByte(s, outputLineBreak); i >= 0 {
		return s[i+truncatedFirstLineOffset:], true
	}
	// One long line: at least start on a whole character, since the cut may
	// have split a multi-byte one.
	for s != "" && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return s, true
}
