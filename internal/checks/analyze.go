package checks

import (
	"fmt"
	"regexp"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/severity"
)

// Analyze stream identifiers accepted by command output analysis rules.
const (
	AnalyzeStreamBoth   = "both"
	AnalyzeStreamStdout = "stdout"
	AnalyzeStreamStderr = "stderr"
	// AnalyzeStreamSummary is the user-facing list of analysis stream values.
	AnalyzeStreamSummary = AnalyzeStreamStdout + ", " + AnalyzeStreamStderr + " or " + AnalyzeStreamBoth
	// AnalyzeExportStreamSummary is the user-facing list of export stream values.
	AnalyzeExportStreamSummary = AnalyzeStreamStdout + " or " + AnalyzeStreamStderr
)

// parseAnalyzeGrade reads a rule's `severity:`. An `ok` rule grades its match
// as benign, which is the unset level: it ranks below every real severity, so a
// whitelisted line can never raise the check's grade.
func parseAnalyzeGrade(s string) (severity.Level, bool) {
	if s == AnalyzeSeverityOK {
		return "", true
	}
	return severity.Parse(s)
}

// analyzeRule is one compiled pattern rule. An unset grade is an `ok` rule.
type analyzeRule struct {
	id     string
	re     *regexp.Regexp
	grade  severity.Level
	stream string
}

// outputAnalyzer holds a check's resolved, compiled rule list.
type outputAnalyzer struct{ rules []analyzeRule }

// Active reports whether there is anything to analyze.
func (a *outputAnalyzer) Active() bool { return a != nil && len(a.rules) > 0 }

// Analyze classifies stdout/stderr. Per non-empty line, the first matching rule
// wins (an `ok` match whitelists that line); the check's grade is the max over
// all lines. It returns that grade — unset when nothing but benign lines
// matched — and the id + line of the first rule that reached it (for the result
// message).
func (a *outputAnalyzer) Analyze(stdout, stderr string) (grade severity.Level, id, line string) {
	scan := func(text, stream string) {
		for ln := range strings.SplitSeq(text, checkLineSeparator) {
			ln = strings.TrimRight(ln, "\r")
			if ln == "" {
				continue
			}
			for _, r := range a.rules {
				if r.stream != AnalyzeStreamBoth && r.stream != stream {
					continue
				}
				if r.re.MatchString(ln) {
					if r.grade.Rank() > grade.Rank() {
						grade, id, line = r.grade, r.id, ln
					}
					break // first match wins for this line
				}
			}
		}
	}
	scan(stdout, AnalyzeStreamStdout)
	scan(stderr, AnalyzeStreamStderr)
	return grade, id, line
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
		return nil, CheckKeyAnalyze + mustBeMappingSuffix
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
			return nil, analyzeRuleIndex(i) + mustBeMappingSuffix
		}
		id := cfgval.AsString(rm[CheckKeyID])
		if id == "" {
			return nil, analyzeRuleIndex(i) + " is missing an id"
		}
		if seen[id] {
			return nil, fmt.Sprintf("%s has a duplicate rule id %q", CheckKeyAnalyze, id)
		}
		seen[id] = true
		grade, ok := parseAnalyzeGrade(cfgval.AsString(rm[CheckKeySeverity]))
		if !ok {
			return nil, fmt.Sprintf("%s severity must be %s", analyzeRuleID(id), AnalyzeSeveritySummary)
		}
		stream := cfgval.AsString(rm[CheckKeyStream])
		if stream == "" {
			stream = AnalyzeStreamBoth
		}
		if stream != AnalyzeStreamBoth && stream != AnalyzeStreamStdout && stream != AnalyzeStreamStderr {
			return nil, fmt.Sprintf("%s stream must be %s", analyzeRuleID(id), AnalyzeStreamSummary)
		}
		match := cfgval.AsString(rm[CheckKeyMatch])
		if match == "" {
			return nil, analyzeRuleID(id) + " is missing a match"
		}
		re, err := regexp.Compile(match)
		if err != nil {
			return nil, fmt.Sprintf("%s has an invalid regex: %v", analyzeRuleID(id), err)
		}
		a.rules = append(a.rules, analyzeRule{id: id, re: re, grade: grade, stream: stream})
	}
	return a, ""
}

func analyzeRuleIndex(index int) string {
	return fmt.Sprintf("%s rule %d", CheckKeyAnalyze, index)
}

func analyzeRuleID(id string) string {
	return fmt.Sprintf("%s rule %q", CheckKeyAnalyze, id)
}
