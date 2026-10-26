package checks

import (
	"errors"
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

// ValidateAnalyzer validates resolved rules without running the command. The
// builder uses the same parser so malformed rules cannot become an inert check.
func ValidateAnalyzer(entry map[string]any) error {
	_, warning := parseAnalyzer(entry)
	if warning != "" {
		return errors.New(warning)
	}
	return nil
}

// parseAnalyzer consumes the resolved rules after config expands use/silence.
// It collects every diagnostic for config validation and never returns a partial
// analyzer: dropping an invalid rule could change which match wins.
func parseAnalyzer(entry map[string]any) (*outputAnalyzer, string) {
	m, present, ok := cfgval.MapAt(entry, CheckKeyAnalyze)
	if !present {
		return nil, ""
	}
	if !ok {
		return nil, CheckKeyAnalyze + mustBeMappingSuffix
	}
	raw, ok := m[CheckKeyRules].([]any)
	if !ok {
		if _, present := m[CheckKeyRules]; present {
			return nil, "analyze.rules must be a list"
		}
		return nil, ""
	}
	if len(raw) == 0 {
		return nil, ""
	}
	a := &outputAnalyzer{}
	seen := map[string]bool{}
	var errs []string
	for i, item := range raw {
		rm, ok := item.(map[string]any)
		if !ok {
			errs = append(errs, analyzeRuleIndex(i)+mustBeMappingSuffix)
			continue
		}
		id := cfgval.AsString(rm[CheckKeyID])
		if id == "" {
			errs = append(errs, analyzeRuleIndex(i)+" is missing an id")
		} else if seen[id] {
			errs = append(errs, fmt.Sprintf("%s has a duplicate rule id %q", CheckKeyAnalyze, id))
		}
		seen[id] = true
		grade, ok := parseAnalyzeGrade(cfgval.AsString(rm[CheckKeySeverity]))
		if !ok {
			errs = append(errs, fmt.Sprintf("%s severity must be %s", analyzeRuleID(id), AnalyzeSeveritySummary))
		}
		stream := cfgval.AsString(rm[CheckKeyStream])
		if stream == "" {
			stream = AnalyzeStreamBoth
		}
		if stream != AnalyzeStreamBoth && stream != AnalyzeStreamStdout && stream != AnalyzeStreamStderr {
			errs = append(errs, fmt.Sprintf("%s stream must be %s", analyzeRuleID(id), AnalyzeStreamSummary))
		}
		match := cfgval.AsString(rm[CheckKeyMatch])
		if match == "" {
			errs = append(errs, analyzeRuleID(id)+" is missing a match")
			continue
		}
		re, err := regexp.Compile(match)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s has an invalid regex: %v", analyzeRuleID(id), err))
			continue
		}
		a.rules = append(a.rules, analyzeRule{id: id, re: re, grade: grade, stream: stream})
	}
	if len(errs) > 0 {
		return nil, strings.Join(errs, "\n")
	}
	return a, ""
}

func analyzeRuleIndex(index int) string {
	return fmt.Sprintf("%s rule %d", CheckKeyAnalyze, index)
}

func analyzeRuleID(id string) string {
	return fmt.Sprintf("%s rule %q", CheckKeyAnalyze, id)
}
