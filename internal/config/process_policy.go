package config

import (
	"fmt"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/process"
)

// ProcessIdentityRule is one validated executable identity from a mapping of
// named rules: a process_policy allow entry or an unowned_processes ignore
// entry. Cmd can only narrow the exact executable and user match applied by
// the daemon.
type ProcessIdentityRule struct {
	Name string
	Exe  string
	Cmd  *regexp.Regexp
}

// ProcessPolicyAllow is the process_policy name of a ProcessIdentityRule.
type ProcessPolicyAllow = ProcessIdentityRule

// ProcessIdentityRuleError identifies one invalid field below a rule mapping.
// PathSuffix is appended to the caller's canonical check.<key> path.
type ProcessIdentityRuleError struct {
	CheckType  string
	Key        string
	PathSuffix string
	Problem    string
}

// ProcessPolicyAllowError is the process_policy name of a ProcessIdentityRuleError.
type ProcessPolicyAllowError = ProcessIdentityRuleError

// Error renders an issue for unchecked builder callers that do not have a
// configuration document path.
func (e ProcessIdentityRuleError) Error() string {
	return e.CheckType + " check." + e.Key + e.PathSuffix + " " + e.Problem
}

// ParseProcessPolicyAllows validates and compiles a process_policy allow
// mapping, which must be non-empty: an empty allowlist would make every
// process of the account a violation.
func ParseProcessPolicyAllows(raw any) ([]ProcessPolicyAllow, []ProcessPolicyAllowError) {
	return ParseProcessIdentityRules(raw, checks.CheckTypeProcessPolicy, checks.CheckKeyAllow, true)
}

// ParseProcessIdentityRules validates and compiles a mapping of named
// executable identities shared by configuration validation and the
// fail-closed daemon builders. required rejects an absent or empty mapping;
// otherwise an absent key yields no rules. It reports all independent field
// issues so validation can preserve its aggregate output.
func ParseProcessIdentityRules(raw any, checkType, key string, required bool) ([]ProcessIdentityRule, []ProcessIdentityRuleError) {
	issue := func(suffix, problem string) ProcessIdentityRuleError {
		return ProcessIdentityRuleError{CheckType: checkType, Key: key, PathSuffix: suffix, Problem: problem}
	}
	rawRules, ok := raw.(map[string]any)
	if !ok || len(rawRules) == 0 {
		if raw == nil && !required {
			return nil, nil
		}
		if required {
			return nil, []ProcessIdentityRuleError{issue("", "is required and must be a non-empty mapping")}
		}
		return nil, []ProcessIdentityRuleError{issue("", "must be a non-empty mapping")}
	}

	rules := make([]ProcessIdentityRule, 0, len(rawRules))
	var issues []ProcessIdentityRuleError
	for _, name := range slices.Sorted(maps.Keys(rawRules)) {
		rule, ruleIssues := parseProcessIdentityRule(name, rawRules[name])
		for i := range ruleIssues {
			ruleIssues[i].CheckType, ruleIssues[i].Key = checkType, key
		}
		issues = append(issues, ruleIssues...)
		if len(ruleIssues) == 0 {
			rules = append(rules, rule)
		}
	}
	return rules, issues
}

func parseProcessIdentityRule(name string, raw any) (ProcessIdentityRule, []ProcessIdentityRuleError) {
	suffix := "." + name
	rawAllow, ok := raw.(map[string]any)
	if !ok {
		return ProcessIdentityRule{}, []ProcessIdentityRuleError{{PathSuffix: suffix, Problem: "must be a mapping"}}
	}

	issues := unsupportedProcessPolicyAllowFields(rawAllow, suffix)
	exe, exeIssue := processPolicyAllowExecutable(rawAllow, suffix)
	if exeIssue != nil {
		issues = append(issues, *exeIssue)
	}
	cmd, commandIssues := processPolicyAllowCommand(rawAllow, suffix)
	issues = append(issues, commandIssues...)
	return ProcessIdentityRule{Name: name, Exe: exe, Cmd: cmd}, issues
}

func unsupportedProcessPolicyAllowFields(rawAllow map[string]any, suffix string) []ProcessPolicyAllowError {
	var issues []ProcessPolicyAllowError
	for key := range unknownBlockKeys(rawAllow, processPolicyAllowKeys) {
		issues = append(issues, ProcessPolicyAllowError{PathSuffix: suffix + "." + key, Problem: "is not supported"})
	}
	return issues
}

func processPolicyAllowExecutable(rawAllow map[string]any, suffix string) (string, *ProcessPolicyAllowError) {
	exe := cfgval.String(rawAllow[checks.CheckKeyExe])
	path := suffix + "." + checks.CheckKeyExe
	switch {
	case exe == "":
		return exe, &ProcessPolicyAllowError{PathSuffix: path, Problem: "is required"}
	case !filepath.IsAbs(exe) || filepath.Clean(exe) != exe:
		return exe, &ProcessPolicyAllowError{PathSuffix: path, Problem: "must be a clean absolute resolved executable path"}
	default:
		return exe, nil
	}
}

func processPolicyAllowCommand(rawAllow map[string]any, suffix string) (*regexp.Regexp, []ProcessPolicyAllowError) {
	rawCommand, present := rawAllow[process.SelectorKeyCmd]
	if !present {
		return nil, nil
	}
	command, ok := rawCommand.(string)
	path := suffix + "." + process.SelectorKeyCmd
	if !ok || command == "" {
		return nil, []ProcessPolicyAllowError{{PathSuffix: path, Problem: "must be a non-empty anchored RE2 expression"}}
	}

	var issues []ProcessPolicyAllowError
	if !strings.HasPrefix(command, "^") || !strings.HasSuffix(command, "$") {
		issues = append(issues, ProcessPolicyAllowError{PathSuffix: path, Problem: "must be anchored with ^ and $"})
	}
	// The ^…$ check above is textual: `^a|b$` passes it, yet its top-level
	// alternation leaves each branch anchored at one end only. Wrapping the
	// whole expression anchors every branch at both ends.
	compiled, err := regexp.Compile(`^(?:` + command + `)$`)
	if err != nil {
		issues = append(issues, ProcessPolicyAllowError{PathSuffix: path, Problem: fmt.Sprintf("is invalid: %v", err)})
		return nil, issues
	}
	return compiled, issues
}

var processPolicyAllowKeys = set(checks.CheckKeyExe, process.SelectorKeyCmd)
