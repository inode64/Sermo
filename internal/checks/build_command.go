package checks

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
)

// buildCommandCheck builds a check that runs a command and asserts its exit code.
func buildCommandCheck(b base, entry map[string]any, runner execx.Runner) (Check, string) {
	argv := cfgval.StringArray(entry[CheckKeyCommand])
	if len(argv) == 0 {
		return nil, "command check requires a non-empty command array"
	}
	expect := []int{CommandDefaultExpectedExit}
	if v, ok := cfgval.IntList(entry[CheckKeyExpectExit]); ok {
		expect = v
	}
	stdout, warn := ParseOutputMatcher(entry[CheckKeyExpectStdout])
	if warn != "" {
		return nil, "command check expect_stdout " + warn
	}
	stderr, warn := ParseOutputMatcher(entry[CheckKeyExpectStderr])
	if warn != "" {
		return nil, "command check expect_stderr " + warn
	}
	version, warn := ParseVersionMatcher(entry[CheckKeyVersionMatch])
	if warn != "" {
		return nil, "command check version_match " + warn
	}
	analyzer, warn := parseAnalyzer(entry[CheckKeyAnalyze])
	if warn != "" {
		return nil, "command check " + warn
	}
	exports, warn := parseCommandExports(b.name, entry[CheckKeyExport])
	if warn != "" {
		return nil, "command check " + warn
	}
	c := commandCheck{base: b, runner: runner, argv: argv, user: cfgval.String(entry[CheckKeyUser]), expectExit: expect, stdout: stdout, stderr: stderr, version: version, exports: exports, analyzer: analyzer}
	if c.onChange = cfgval.Bool(entry[CheckKeyOnChange]); c.onChange {
		c.changeLevel, _ = cfgval.Int(entry[CheckKeyChangeLevel])
		c.state = &cmdState{}
	}
	return c, ""
}

type commandExport struct {
	name         string
	from         string
	trim         bool
	defaultValue string
	regex        *regexp.Regexp
	shortVersion bool
}

func parseCommandExports(checkName string, raw any) ([]commandExport, string) {
	exports := map[string]commandExport{}
	switch checkName {
	case DataKeyVersion:
		exports[DataKeyVersion] = defaultCommandExport(DataKeyVersion)
		short := defaultCommandExport(DataKeyVersionShort)
		short.shortVersion = true
		exports[DataKeyVersionShort] = short
	case DataKeyVersionShort:
		exports[checkName] = defaultCommandExport(checkName)
	}
	if raw == nil {
		return sortedCommandExports(exports), ""
	}
	specs, ok := raw.(map[string]any)
	if !ok {
		return nil, CheckKeyExport + " must be a mapping of variable name -> export rule"
	}
	for _, name := range slices.Sorted(maps.Keys(specs)) {
		spec, ok := specs[name].(map[string]any)
		if !ok {
			return nil, commandExportPath(name) + mustBeMappingSuffix
		}
		e := defaultCommandExport(name)
		if from := cfgval.String(spec[CheckKeyFrom]); from != "" {
			e.from = from
		}
		switch e.from {
		case AnalyzeStreamStdout, AnalyzeStreamStderr:
		default:
			return nil, commandExportPath(name, CheckKeyFrom) + " must be " + AnalyzeExportStreamSummary
		}
		if rawTrim, present := spec[CheckKeyTrim]; present {
			v, ok := rawTrim.(bool)
			if !ok {
				return nil, commandExportPath(name, CheckKeyTrim) + " must be a boolean"
			}
			e.trim = v
		}
		if rawDefault, present := spec[CheckKeyDefault]; present {
			e.defaultValue = cfgval.String(rawDefault)
		}
		if rawRegex, present := spec[CheckKeyRegex]; present {
			pattern := cfgval.String(rawRegex)
			if pattern == "" {
				return nil, commandExportPath(name, CheckKeyRegex) + " must be non-empty"
			}
			re, err := regexp.Compile(pattern)
			if err != nil {
				return nil, commandExportPath(name, CheckKeyRegex) + " is invalid: " + err.Error()
			}
			e.regex = re
		}
		exports[name] = e
	}
	return sortedCommandExports(exports), ""
}

func commandExportPath(name string, fields ...string) string {
	const commandExportPathPrefixParts = 2

	parts := make([]string, 0, len(fields)+commandExportPathPrefixParts)
	parts = append(parts, CheckKeyExport, name)
	parts = append(parts, fields...)
	return strings.Join(parts, ".")
}

func defaultCommandExport(name string) commandExport {
	return commandExport{name: name, from: AnalyzeStreamStdout, trim: true}
}

var commandShortVersionRE = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)
var commandShortIntegerVersionRE = regexp.MustCompile(`(?i)\b(?:version|v)\s*:?\s*(\d+)\b`)

const (
	commandRegexFullMatchGroup     = 0
	commandRegexFirstCaptureGroup  = 1
	commandRegexMinCapturedMatches = 2
)

func commandShortVersion(s string) string {
	if dotted := commandShortVersionRE.FindString(s); dotted != "" {
		return dotted
	}
	if match := commandShortIntegerVersionRE.FindStringSubmatch(s); len(match) >= commandRegexMinCapturedMatches {
		return match[commandRegexFirstCaptureGroup]
	}
	return ""
}

func sortedCommandExports(exports map[string]commandExport) []commandExport {
	if len(exports) == 0 {
		return nil
	}
	out := make([]commandExport, 0, len(exports))
	for _, name := range slices.Sorted(maps.Keys(exports)) {
		out = append(out, exports[name])
	}
	return out
}

func (e commandExport) value(stdout, stderr string) string {
	source := stdout
	if e.from == AnalyzeStreamStderr {
		source = stderr
	}
	value := source
	if e.regex != nil {
		match := e.regex.FindStringSubmatch(source)
		switch {
		case match == nil:
			value = e.defaultValue
		case len(match) >= commandRegexMinCapturedMatches:
			value = match[commandRegexFirstCaptureGroup]
		default:
			value = match[commandRegexFullMatchGroup]
		}
	} else if e.shortVersion {
		value = commandShortVersion(source)
		if value == "" {
			value = e.defaultValue
		}
	}
	if e.trim {
		value = strings.TrimSpace(value)
	}
	return value
}
