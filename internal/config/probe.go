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
	yamlAssignSep       = ":"
	// confdAssignPadding is what a host config file may put between a key and its
	// assignment separator. Exim writes `key = value`; OpenRC and YAML write no
	// padding at all.
	confdAssignPadding  = " \t"
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
	data, err := os.ReadFile(path) //nolint:gosec // G304: optional host file from catalog/os-select paths
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

// configAssignSeps are the assignment forms configKeyValue accepts after a key,
// in the two shapes host config files actually use: an OpenRC/dnsmasq
// `KEY="val"` and a YAML block-mapping `key: val`. A key alone on the line is a
// bare feature flag and is handled before these.
var configAssignSeps = []string{confdAssignSep, yamlAssignSep}

// configKeyValue returns the value of a KEY="val", `key: val` or `key = val`
// assignment, or an empty value for a bare KEY feature flag (which a YAML `key:`
// opening a nested block also is). Optional spaces or tabs may sit between the
// key and its separator, which is how exim.conf writes its options. Surrounding
// quotes are stripped; a trailing comment is not, so prefer `matches:` over
// `equals:` on a file that writes them. ok=false when the file is unreadable or
// the key is absent.
func configKeyValue(path, key string) (string, bool) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: OpenRC conf.d path from catalog service unit
	if err != nil {
		return "", false
	}
	for line := range strings.SplitSeq(string(data), configLineSeparator) {
		line = strings.TrimSpace(line)
		if line == key {
			return "", true
		}
		rest, cut := strings.CutPrefix(line, key)
		if !cut {
			continue
		}
		// Skip padding only after the key matched at line start, so a longer key
		// that merely shares a prefix (`portable=1` against `port`) still needs a
		// separator right where the key ends and cannot match.
		rest = strings.TrimLeft(rest, confdAssignPadding)
		for _, sep := range configAssignSeps {
			if value, ok := strings.CutPrefix(rest, sep); ok {
				return strings.Trim(strings.TrimSpace(value), confdQuoteTrimSet), true
			}
		}
	}
	return "", false
}
