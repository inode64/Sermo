package config

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

// ServiceBinary returns the resolved binary path of a daemon: its preflight
// `binary` check path when present, then a namespaced one an app contributed
// (`rspamd-binary`), then the `binary` variable.
func ServiceBinary(tree map[string]any) string {
	if pf, ok := tree[SectionPreflight].(map[string]any); ok {
		if p := PreflightPath(pf, checks.CheckTypeBinary); p != "" {
			return p
		}
		if p := firstNamespacedBinaryPath(pf); p != "" {
			return p
		}
	}
	if vars, ok := tree[SectionVariables].(map[string]any); ok {
		if p := cfgval.AsString(vars[VariableKeyBinary]); p != "" {
			return p
		}
	}
	return DocumentBinary(tree)
}

func firstNamespacedBinaryPath(preflight map[string]any) string {
	for _, prefix := range NamespacedBinaryPrefixes(preflight) {
		if p := PreflightPath(preflight, prefix+"-"+checks.CheckTypeBinary); p != "" {
			return p
		}
	}
	return ""
}

// PreflightPath returns the resolved path of a named preflight check.
func PreflightPath(preflight map[string]any, name string) string {
	entry, ok := preflight[name].(map[string]any)
	if !ok {
		return ""
	}
	return cfgval.AsString(entry[checks.CheckKeyPath])
}

// NamespacedBinaryPrefixes returns, sorted, the prefixes of the `<prefix>-binary`
// preflight checks that linked apps contribute to a service.
func NamespacedBinaryPrefixes(preflight map[string]any) []string {
	prefixes := make([]string, 0, len(preflight))
	for key, raw := range preflight {
		prefix, ok := strings.CutSuffix(key, "-"+checks.CheckTypeBinary)
		if !ok || prefix == "" {
			continue
		}
		entry, ok := raw.(map[string]any)
		if !ok || cfgval.AsString(entry[checks.CheckKeyType]) != checks.CheckTypeBinary || cfgval.AsString(entry[checks.CheckKeyPath]) == "" {
			continue
		}
		prefixes = append(prefixes, prefix)
	}
	slices.Sort(prefixes)
	return prefixes
}

// expandLibrariesBinary gives a service's `libraries` checks that name no
// binary the service's own executable. A policy shared by many services, or
// generated for one, then does not pin the file a package versions
// (rspamd-4.1.1, gone after the upgrade to 4.2.3): the check resolves the
// service's path through its links on every run.
func expandLibrariesBinary(tree map[string]any) []string {
	var errs []string
	binary := ""
	for _, section := range []string{sectionPreflight, sectionChecks} {
		entries, _ := tree[section].(map[string]any)
		for _, name := range slices.Sorted(maps.Keys(entries)) {
			entry, ok := entries[name].(map[string]any)
			if !ok || cfgval.AsString(entry[checks.CheckKeyType]) != checks.CheckTypeLibraries {
				continue
			}
			if _, present := entry[checks.CheckKeyBinary]; present {
				continue
			}
			if binary == "" {
				binary = ServiceBinary(tree)
			}
			if binary == "" {
				errs = append(errs, fmt.Sprintf("%s.%s: a libraries check without binary needs a service that declares one", section, name))
				continue
			}
			entry[checks.CheckKeyBinary] = binary
		}
	}
	return errs
}
