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

// ProcessPolicyAllowIssue identifies one invalid field below check.allow.
// PathSuffix is appended to the caller's canonical check.allow path.
type ProcessPolicyAllowIssue struct {
	PathSuffix string
	Problem    string
}

// Error renders an issue for unchecked builder callers that do not have a
// configuration document path.
func (i ProcessPolicyAllowIssue) Error() string {
	return "process_policy check." + checks.CheckKeyAllow + i.PathSuffix + " " + i.Problem
}

// ParseProcessPolicyAllows validates and compiles the allow mapping shared by
// configuration validation and the fail-closed daemon builder. It reports all
// independent field issues so validation can preserve its aggregate output.
func ParseProcessPolicyAllows(raw any) ([]ProcessPolicyAllow, []ProcessPolicyAllowIssue) {
	rawAllows, ok := raw.(map[string]any)
	if !ok || len(rawAllows) == 0 {
		return nil, []ProcessPolicyAllowIssue{{Problem: "is required and must be a non-empty mapping"}}
	}

	allows := make([]ProcessPolicyAllow, 0, len(rawAllows))
	var issues []ProcessPolicyAllowIssue
	for _, name := range slices.Sorted(maps.Keys(rawAllows)) {
		suffix := "." + name
		rawAllow, ok := rawAllows[name].(map[string]any)
		if !ok {
			issues = append(issues, ProcessPolicyAllowIssue{PathSuffix: suffix, Problem: "must be a mapping"})
			continue
		}

		valid := true
		for _, key := range slices.Sorted(maps.Keys(rawAllow)) {
			if key != checks.CheckKeyExe && key != process.SelectorKeyCmd {
				issues = append(issues, ProcessPolicyAllowIssue{PathSuffix: suffix + "." + key, Problem: "is not supported"})
				valid = false
			}
		}

		exe := cfgval.String(rawAllow[checks.CheckKeyExe])
		switch {
		case exe == "":
			issues = append(issues, ProcessPolicyAllowIssue{PathSuffix: suffix + "." + checks.CheckKeyExe, Problem: "is required"})
			valid = false
		case !filepath.IsAbs(exe) || filepath.Clean(exe) != exe:
			issues = append(issues, ProcessPolicyAllowIssue{PathSuffix: suffix + "." + checks.CheckKeyExe, Problem: "must be a clean absolute resolved executable path"})
			valid = false
		}

		allow := ProcessPolicyAllow{Name: name, Exe: exe}
		if rawCommand, present := rawAllow[process.SelectorKeyCmd]; present {
			command, commandOK := rawCommand.(string)
			if !commandOK || command == "" {
				issues = append(issues, ProcessPolicyAllowIssue{PathSuffix: suffix + "." + process.SelectorKeyCmd, Problem: "must be a non-empty anchored RE2 expression"})
				valid = false
			} else {
				if !strings.HasPrefix(command, "^") || !strings.HasSuffix(command, "$") {
					issues = append(issues, ProcessPolicyAllowIssue{PathSuffix: suffix + "." + process.SelectorKeyCmd, Problem: "must be anchored with ^ and $"})
					valid = false
				}
				compiled, err := regexp.Compile(command)
				if err != nil {
					issues = append(issues, ProcessPolicyAllowIssue{PathSuffix: suffix + "." + process.SelectorKeyCmd, Problem: fmt.Sprintf("is invalid: %v", err)})
					valid = false
				} else {
					allow.Cmd = compiled
				}
			}
		}
		if valid {
			allows = append(allows, allow)
		}
	}
	return allows, issues
}
