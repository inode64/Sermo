// Package output formats captured stdout, stderr and probe text for stable
// comparisons and compact diagnostics.
package output

import "strings"

// Trim removes surrounding whitespace from captured command, SQL, protocol and
// hook text while preserving meaningful internal line breaks.
func Trim(s string) string {
	return strings.TrimSpace(s)
}

// FirstNonEmptyLine returns the first non-empty line of s, trimmed.
func FirstNonEmptyLine(s string) string {
	clean := Trim(s)
	for _, line := range strings.Split(clean, "\n") {
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
		{label: "stdout", text: stdout},
		{label: "stderr", text: stderr},
	} {
		if section := streamSection(stream.label, stream.text); section != "" {
			parts = append(parts, section)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return boundTail(strings.Join(parts, "\n"))
}

func streamSection(label, text string) string {
	if s := Trim(text); s != "" {
		return label + ":\n" + s
	}
	return ""
}

func boundTail(s string) string {
	truncated := false
	lines := strings.Split(s, "\n")
	if len(lines) > boundedMaxLines {
		lines = lines[len(lines)-boundedMaxLines:]
		truncated = true
	}
	s = strings.Join(lines, "\n")
	if len(s) > boundedMaxBytes {
		s = s[len(s)-boundedMaxBytes:]
		// Drop a partial first line left by the byte cut.
		if i := strings.IndexByte(s, '\n'); i >= 0 {
			s = s[i+1:]
		}
		truncated = true
	}
	if truncated {
		return "… (truncated)\n" + s
	}
	return s
}
