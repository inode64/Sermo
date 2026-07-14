package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"sermo/internal/cfgval"
	"strings"
)

const (
	directiveKeyIndex   = 0
	directiveValueIndex = 1
	directiveMinFields  = directiveValueIndex + 1
	confdAssignSep      = "="
	confdQuoteTrimSet   = `"'`
	configLineSeparator = "\n"

	patternCaptureGroup     = 1
	patternCaptureMinGroups = patternCaptureGroup + 1
)

// probe.go reads host config files at load/resolve time. Two consumers share it:
// `enable_if` (a boolean predicate that keeps or prunes a document branch) and
// from_file variables (a value extracted into ${var}). A missing file or
// unmatched key is not an error; malformed extraction specs are validation
// errors.

// extractFileValue reads path and pulls a single value out of it. With
// `pattern:` it returns capture group 1 of the first regex match; with
// `directive:` it returns the token after the named key on the first matching
// "key value" line (OpenVPN/sshd style). Returns ok=false when the file cannot
// be read or nothing matches, so the caller can fall back to a default.
func extractFileValue(path string, spec map[string]any) (string, bool, error) {
	data, ok := readOptionalFile(path)
	if !ok {
		return "", false, nil
	}
	if pat := cfgval.String(spec[varKeyPattern]); pat != "" {
		re, err := regexp.Compile(pat)
		if err != nil {
			return "", false, fmt.Errorf("pattern is not a valid regex: %w", err)
		}
		if re.NumSubexp() < 1 {
			return "", false, errors.New("pattern must define at least one capture group")
		}
		if sub := re.FindSubmatch(data); len(sub) >= patternCaptureMinGroups {
			return string(sub[patternCaptureGroup]), true, nil
		}
		return "", false, nil
	}
	if key := cfgval.String(spec[varKeyDirective]); key != "" {
		value, ok := directiveValue(data, key)
		return value, ok, nil
	}
	return "", false, nil
}

func readOptionalFile(path string) ([]byte, bool) {
	data, err := os.ReadFile(path)
	return data, err == nil
}

// directiveValue returns the first value of a "key value" directive line, where
// key and value are whitespace-separated (e.g. `port 1194`). Comment and blank
// lines never match because their first field is not the key.
func directiveValue(data []byte, key string) (string, bool) {
	for line := range strings.SplitSeq(string(data), configLineSeparator) {
		fields := strings.Fields(line)
		if len(fields) >= directiveMinFields && fields[directiveKeyIndex] == key {
			return fields[directiveValueIndex], true
		}
	}
	return "", false
}

// confdValue returns the value of a shell-style KEY="val" assignment (the
// /etc/conf.d/<service> convention), with surrounding quotes stripped. ok=false
// when the file is unreadable or the key is absent.
func confdValue(path, key string) (string, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(data), configLineSeparator) {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, key+confdAssignSep)
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(rest), confdQuoteTrimSet), true
	}
	return "", false
}
