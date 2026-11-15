// Package output formats captured stdout, stderr and probe text for stable
// comparisons and compact diagnostics.
package output

import (
	"regexp"
	"slices"
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

// pythonTraceback opens Python's report of an uncaught exception, which names
// the exception after the stack frames.
const pythonTraceback = "Traceback (most recent call last):"

// pythonExceptionLine matches the line a traceback names its exception on: a
// class whose name ends the way Python's do (ValueError, KeyboardInterrupt,
// ExceptionGroup), with or without a message.
var pythonExceptionLine = regexp.MustCompile(`^[A-Za-z_][\w.]*(?:Error|Exception|Warning|Interrupt|Exit|Group|Iteration)(?:: .*)?$`)

// tracebackFrameChars draw the boxes an ExceptionGroup traceback nests its
// sub-exceptions in.
const tracebackFrameChars = "|+- "

// cause returns the line a failing command states its reason on: the first
// non-empty line, joined with the next non-empty one when it only introduces
// it — Apache's configtest reports "AH00526: Syntax error on line 3 of FILE:"
// and the directive it rejected on the line below. A Python program that died
// of an exception states it last ("ModuleNotFoundError: No module named …").
func cause(s string) string {
	reason, _ := causeOf(s)
	return reason
}

// causeOf is cause plus whether the reason is only an introducer that no line
// followed, a sentence announcing a reason that s never gives.
func causeOf(s string) (reason string, introducerOnly bool) {
	if exception, ok := pythonException(s); ok {
		return exception, false
	}
	var first string
	for line := range strings.SplitSeq(s, outputLineSeparator) {
		t := strings.TrimSpace(line)
		switch {
		case t == "":
			continue
		case first == "":
			if !strings.HasSuffix(t, causeIntroducer) {
				return t, false
			}
			first = t
		default:
			return first + " " + t, false
		}
	}
	return first, first != ""
}

// pythonException returns the exception a Python traceback in s ended with,
// and whether s holds one. It is the last line shaped like an exception, so
// notes and the continuation of a message printed after it, and the frame of
// an ExceptionGroup, do not hide it; one that never matches falls back to the
// last line.
func pythonException(s string) (string, bool) {
	if !strings.Contains(s, pythonTraceback) {
		return "", false
	}
	for _, line := range slices.Backward(strings.Split(s, outputLineSeparator)) {
		if t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), tracebackFrameChars)); pythonExceptionLine.MatchString(t) {
			return t, true
		}
	}
	return lastNonEmptyLine(s), true
}

func lastNonEmptyLine(s string) string {
	for _, line := range slices.Backward(strings.Split(s, outputLineSeparator)) {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}
	return ""
}

// FailureCause is a failing command's stated reason: Cause of stderr, or of
// stdout for a command that reports errors there. A stderr that only
// introduces a reason it never gives defers to stdout: Gentoo's apache2ctl
// configtest writes "apache2 has detected an error in your setup:" to stderr
// and the AH00526 syntax error itself to stdout.
func FailureCause(stdout, stderr string) string {
	errCause, introducerOnly := causeOf(stderr)
	if errCause != "" && !introducerOnly {
		return errCause
	}
	if outCause := cause(stdout); outCause != "" {
		return outCause
	}
	return errCause
}

// The words in which a program reports that it died before doing its work
// because a library it loads is missing: the dynamic loader names an absent
// shared object or symbol version, Python the module it failed to import and
// Perl the module it did not find on its search path.
const (
	loaderMissingObject  = "error while loading shared libraries: "
	loaderMissingVersion = "' not found (required by "
	pythonImportError    = "ImportError: "
	pythonModuleNotFound = "ModuleNotFoundError: "
	perlMissingModule    = "Can't locate "
	perlModuleSearchPath = " in @INC"
)

// MissingLibrary returns the line in which a failed program reports a library
// it could not load, or "" when its output shows no such death. Such a program
// observed nothing: what it was asked to probe may be perfectly well.
func MissingLibrary(stdout, stderr string) string {
	if line := missingLibrary(stderr); line != "" {
		return line
	}
	return missingLibrary(stdout)
}

func missingLibrary(s string) string {
	if last, ok := pythonException(s); ok {
		// Only the exception that ended the program counts: an import error
		// caught and reported further up the traceback is not its cause.
		if strings.HasPrefix(last, pythonModuleNotFound) || strings.HasPrefix(last, pythonImportError) {
			return last
		}
		return ""
	}
	for line := range strings.SplitSeq(s, outputLineSeparator) {
		t := strings.TrimSpace(line)
		if strings.Contains(t, loaderMissingObject) || strings.Contains(t, loaderMissingVersion) ||
			(strings.HasPrefix(t, perlMissingModule) && strings.Contains(t, perlModuleSearchPath)) {
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
