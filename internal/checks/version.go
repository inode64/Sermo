package checks

import (
	"regexp"
	"strings"

	"sermo/internal/cfgval"
)

const (
	reservedCommandSectionPreflight = "preflight"
	reservedCommandSectionCommands  = "commands"
	ntpVersionPrefix                = "ntpd "
	shortVersionCaptureGroup        = 1
)

// Version-level names accepted by version.on_change.
const (
	VersionLevelMajor = "major"
	VersionLevelMinor = "minor"
	VersionLevelPatch = "patch"
	// VersionLevelSummary is the user-facing list of accepted version levels.
	VersionLevelSummary = VersionLevelMajor + ", " + VersionLevelMinor + ", " + VersionLevelPatch
)

// Version-level component counts used when truncating version_short values.
const (
	VersionLevelMajorComponents = 1
	VersionLevelMinorComponents = 2
	VersionLevelPatchComponents = 3
)

// ReservedCommandEntry returns the resolved reserved-command entry for key
// ("health", "version" or "version_short"): preflight.<key> takes precedence
// over commands.<key>, and an entry only counts when it carries a non-empty
// command argv. Shared by the apps listings (appinspect) and the
// version.on_change monitor (app) so the precedence rule lives in one place.
// nil when neither section declares one.
func ReservedCommandEntry(tree map[string]any, key string) map[string]any {
	for _, src := range []string{reservedCommandSectionPreflight, reservedCommandSectionCommands} {
		if section, ok := tree[src].(map[string]any); ok {
			if entry, ok := section[key].(map[string]any); ok && len(cfgval.StringList(entry[CheckKeyCommand])) > 0 {
				return entry
			}
		}
	}
	return nil
}

// VersionCommandEntry keeps the older version-specific name for callers that
// consume the version monitor. New reserved command lookups should use
// ReservedCommandEntry.
func VersionCommandEntry(tree map[string]any, key string) map[string]any {
	return ReservedCommandEntry(tree, key)
}

// shortVersionRE captures the first dotted numeric version in a raw version
// line: a `major.minor` with an optional `.patch`. The first capture group is
// the normalized value; the surrounding match may include a non-version prefix
// character so formats such as `go1.26.2` still parse while suffixes and extra
// build components are left out.
var shortVersionRE = regexp.MustCompile(`(?:^|[^0-9.])([0-9]+\.[0-9]+(?:\.[0-9]+)?)`)

var shortVersionSpecificREs = []*regexp.Regexp{
	regexp.MustCompile(`\bisc-dh(?:client|cpd)-([0-9]+\.[0-9]+\.[0-9]+-P[0-9]+)\b`),
	regexp.MustCompile(`\bOpenSSH[_ ]([0-9]+\.[0-9]+p[0-9]+)\b`),
	regexp.MustCompile(`\bNET-SNMP version:\s*([0-9]+\.[0-9]+\.[0-9]+(?:\.[0-9]+|\.pre[0-9]*)?)\b`),
	regexp.MustCompile(`\bNetwork UPS Tools (?:upsd|upsmon)\s+([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)\b`),
	regexp.MustCompile(`\bxinetd\s+([0-9]+\.[0-9]+\.[0-9]+\.[0-9]+)\b`),
}

var ntpPatchVersionRE = regexp.MustCompile(`^([0-9]+\.[0-9]+\.[0-9]+p[0-9]+)(?:@.*)?$`)

// shortIntegerVersionRE covers projects that publish integer-only releases in
// version output, such as "pkexec version 126". It only runs after the dotted
// matcher misses so a line like "systemd 260 (260.1)" still reports "260.1".
var shortIntegerVersionRE = regexp.MustCompile(`(?i)\b(?:version|v)\s*:?\s*([0-9]+)\b`)

// ShortVersion reduces a raw version line (as captured in Report.Version) to
// just its numeric version, keeping at most three components
// (major.minor.patch). It returns the first dotted numeric token found, then a
// guarded integer-only version token, or "" when the line carries no
// recognizable version.
func ShortVersion(s string) string {
	if v := shortNTPVersion(s); v != "" {
		return v
	}
	for _, re := range shortVersionSpecificREs {
		if match := re.FindStringSubmatch(s); len(match) > shortVersionCaptureGroup {
			return match[shortVersionCaptureGroup]
		}
	}
	if match := shortVersionRE.FindStringSubmatch(s); len(match) > shortVersionCaptureGroup {
		return match[shortVersionCaptureGroup]
	}
	if match := shortIntegerVersionRE.FindStringSubmatch(s); len(match) > shortVersionCaptureGroup {
		return match[shortVersionCaptureGroup]
	}
	return ""
}

func shortNTPVersion(s string) string {
	if !strings.HasPrefix(s, ntpVersionPrefix) {
		return ""
	}
	token, _, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(s, ntpVersionPrefix)), " ")
	if token == "" {
		return ""
	}
	if match := ntpPatchVersionRE.FindStringSubmatch(token); len(match) > shortVersionCaptureGroup {
		return match[shortVersionCaptureGroup]
	}
	if strings.Contains(token, "@") {
		if match := shortVersionRE.FindStringSubmatch(token); len(match) > shortVersionCaptureGroup {
			return match[shortVersionCaptureGroup]
		}
		return ""
	}
	return ""
}

// VersionLevel maps a configured granularity name to the number of leading
// numeric components that are significant: major→1, minor→2, patch→3. The bool
// is false for any other name.
func VersionLevel(name string) (int, bool) {
	switch name {
	case VersionLevelMajor:
		return VersionLevelMajorComponents, true
	case VersionLevelMinor:
		return VersionLevelMinorComponents, true
	case VersionLevelPatch:
		return VersionLevelPatchComponents, true
	default:
		return 0, false
	}
}

// TruncateVersion keeps the first `level` dot-separated components of an
// already-short version such as "1.4.2". level<=0 or a "" input returns the
// input unchanged; a level beyond the available components keeps them all.
func TruncateVersion(short string, level int) string {
	if short == "" || level <= 0 {
		return short
	}
	parts := strings.Split(short, ".")
	if level >= len(parts) {
		return short
	}
	return strings.Join(parts[:level], ".")
}
