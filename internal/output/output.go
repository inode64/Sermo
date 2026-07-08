// Package output formats captured stdout, stderr and probe text for stable
// comparisons and compact diagnostics.
package output

import "strings"

const (
	outputLineBreak          = '\n'
	outputLineSeparator      = "\n"
	streamLabelStdout        = "stdout"
	streamLabelStderr        = "stderr"
	streamLabelSeparator     = ":\n"
	truncatedOutputPrefix    = "… (truncated)\n"
	truncatedFirstLineOffset = 1
)

// Trim removes surrounding whitespace from captured command, SQL, protocol and
// hook text while preserving meaningful internal line breaks.
func Trim(s string) string {
	return strings.TrimSpace(s)
}

// FirstNonEmptyLine returns the first non-empty line of s, trimmed.
func FirstNonEmptyLine(s string) string {
	clean := Trim(s)
	for _, line := range strings.Split(clean, outputLineSeparator) {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
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
	if s := Trim(text); s != "" {
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
		s = s[i+truncatedFirstLineOffset:]
	}
	return s, true
}
