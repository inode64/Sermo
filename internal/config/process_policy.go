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

// ProcessPolicyAllow is one validated executable identity from a
// process_policy allow mapping. Cmd can only narrow the exact executable
// and user match applied by the daemon.
type ProcessPolicyAllow struct {
	Name string
	Exe  string
	Cmd  *regexp.Regexp
}

// ProcessPolicyAllowError identifies one invalid field below check.allow.
// PathSuffix is appended to the caller's canonical check.allow path.
type ProcessPolicyAllowError struct {
	PathSuffix string
	Problem    string
}

// Error renders an issue for unchecked builder callers that do not have a
// configuration document path.
func (e ProcessPolicyAllowError) Error() string {
	return "process_policy check." + checks.CheckKeyAllow + e.PathSuffix + " " + e.Problem
}

// ParseProcessPolicyAllows validates and compiles the allow mapping shared by
// configuration validation and the fail-closed daemon builder. It reports all
// independent field issues so validation can preserve its aggregate output.
func ParseProcessPolicyAllows(raw any) ([]ProcessPolicyAllow, []ProcessPolicyAllowError) {
	rawAllows, ok := raw.(map[string]any)
	if !ok || len(rawAllows) == 0 {
		return nil, []ProcessPolicyAllowError{{Problem: "is required and must be a non-empty mapping"}}
	}

	allows := make([]ProcessPolicyAllow, 0, len(rawAllows))
	var issues []ProcessPolicyAllowError
	for _, name := range slices.Sorted(maps.Keys(rawAllows)) {
		allow, allowIssues := parseProcessPolicyAllow(name, rawAllows[name])
		issues = append(issues, allowIssues...)
		if len(allowIssues) == 0 {
			allows = append(allows, allow)
		}
	}
	return allows, issues
}

func parseProcessPolicyAllow(name string, raw any) (ProcessPolicyAllow, []ProcessPolicyAllowError) {
	suffix := "." + name
	rawAllow, ok := raw.(map[string]any)
	if !ok {
		return ProcessPolicyAllow{}, []ProcessPolicyAllowError{{PathSuffix: suffix, Problem: "must be a mapping"}}
	}

	issues := unsupportedProcessPolicyAllowFields(rawAllow, suffix)
	exe, exeIssue := processPolicyAllowExecutable(rawAllow, suffix)
	if exeIssue != nil {
		issues = append(issues, *exeIssue)
	}
	cmd, commandIssues := processPolicyAllowCommand(rawAllow, suffix)
	issues = append(issues, commandIssues...)
	return ProcessPolicyAllow{Name: name, Exe: exe, Cmd: cmd}, issues
}

func unsupportedProcessPolicyAllowFields(rawAllow map[string]any, suffix string) []ProcessPolicyAllowError {
	var issues []ProcessPolicyAllowError
	for _, key := range slices.Sorted(maps.Keys(rawAllow)) {
		if key != checks.CheckKeyExe && key != process.SelectorKeyCmd {
			issues = append(issues, ProcessPolicyAllowError{PathSuffix: suffix + "." + key, Problem: "is not supported"})
		}
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
	compiled, err := regexp.Compile(command)
	if err != nil {
		issues = append(issues, ProcessPolicyAllowError{PathSuffix: path, Problem: fmt.Sprintf("is invalid: %v", err)})
		return nil, issues
	}
	return compiled, issues
}
