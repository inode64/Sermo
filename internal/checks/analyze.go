package checks

import (
	"fmt"
	"regexp"
	"strings"

	"sermo/internal/cfgval"
)

// Severity ranks a pattern match; higher is worse.
type Severity int

// Severity levels assigned by an analyze rule, ordered ok < warning < error.
const (
	SevOK      Severity = iota // benign / whitelist
	SevWarning                 // degraded — maps to an optional (warning) failure
	SevError                   // maps to a required failure
)

// Analyze severity names accepted in YAML and rendered in output.
const (
	AnalyzeSeverityError   = "error"
	AnalyzeSeverityWarning = "warning"
	AnalyzeSeverityOK      = "ok"
)

// Analyze stream identifiers accepted by command output analysis rules.
const (
	AnalyzeStreamBoth   = "both"
	AnalyzeStreamStdout = "stdout"
	AnalyzeStreamStderr = "stderr"
)

func (s Severity) String() string {
	switch s {
	case SevError:
		return AnalyzeSeverityError
	case SevWarning:
		return AnalyzeSeverityWarning
	default:
		return AnalyzeSeverityOK
	}
}

func parseSeverity(s string) (Severity, bool) {
	switch s {
	case AnalyzeSeverityError:
		return SevError, true
	case AnalyzeSeverityWarning:
		return SevWarning, true
	case AnalyzeSeverityOK:
		return SevOK, true
	default:
		return SevOK, false
	}
}

// analyzeRule is one compiled pattern rule.
type analyzeRule struct {
	id       string
	re       *regexp.Regexp
	severity Severity
	stream   string
}

// outputAnalyzer holds a check's resolved, compiled rule list.
type outputAnalyzer struct{ rules []analyzeRule }

// Active reports whether there is anything to analyze.
func (a *outputAnalyzer) Active() bool { return a != nil && len(a.rules) > 0 }

// Analyze classifies stdout/stderr. Per non-empty line, the first matching rule
// wins (an `ok` match whitelists that line); the check's severity is the max
// over all lines. It returns that severity and the id + line of the first rule
// that reached it (for the result message).
func (a *outputAnalyzer) Analyze(stdout, stderr string) (sev Severity, id, line string) {
	scan := func(text, stream string) {
		for _, ln := range strings.Split(text, checkLineSeparator) {
			ln = strings.TrimRight(ln, "\r")
			if ln == "" {
				continue
			}
			for _, r := range a.rules {
				if r.stream != AnalyzeStreamBoth && r.stream != stream {
					continue
				}
				if r.re.MatchString(ln) {
					if r.severity > sev {
						sev, id, line = r.severity, r.id, ln
					}
					break // first match wins for this line
				}
			}
		}
	}
	scan(stdout, AnalyzeStreamStdout)
	scan(stderr, AnalyzeStreamStderr)
	return sev, id, line
}

// parseAnalyzer reads a resolved `analyze` mapping (its `rules` list — `use` and
// `silence` are already consumed by expandAnalyze) into a compiled analyzer. It
// returns the analyzer (nil when absent or ruleless) and a warning string ("" when
// valid) describing the first invalid rule.
func parseAnalyzer(v any) (*outputAnalyzer, string) {
	if v == nil {
		return nil, ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, "analyze must be a mapping"
	}
	raw, ok := m[CheckKeyRules].([]any)
	if !ok || len(raw) == 0 {
		return nil, "" // inert: no rules
	}
	a := &outputAnalyzer{}
	seen := map[string]bool{}
	for i, item := range raw {
		rm, ok := item.(map[string]any)
		if !ok {
			return nil, fmt.Sprintf("analyze rule %d must be a mapping", i)
		}
		id := cfgval.AsString(rm[CheckKeyID])
		if id == "" {
			return nil, fmt.Sprintf("analyze rule %d is missing an id", i)
		}
		if seen[id] {
			return nil, fmt.Sprintf("analyze has a duplicate rule id %q", id)
		}
		seen[id] = true
		sev, ok := parseSeverity(cfgval.AsString(rm[CheckKeySeverity]))
		if !ok {
			return nil, fmt.Sprintf("analyze rule %q severity must be error, warning or ok", id)
		}
		stream := cfgval.AsString(rm[CheckKeyStream])
		if stream == "" {
			stream = AnalyzeStreamBoth
		}
		if stream != AnalyzeStreamBoth && stream != AnalyzeStreamStdout && stream != AnalyzeStreamStderr {
			return nil, fmt.Sprintf("analyze rule %q stream must be stdout, stderr or both", id)
		}
		match := cfgval.AsString(rm[CheckKeyMatch])
		if match == "" {
			return nil, fmt.Sprintf("analyze rule %q is missing a match", id)
		}
		re, err := regexp.Compile(match)
		if err != nil {
			return nil, fmt.Sprintf("analyze rule %q has an invalid regex: %v", id, err)
		}
		a.rules = append(a.rules, analyzeRule{id: id, re: re, severity: sev, stream: stream})
	}
	return a, ""
}
