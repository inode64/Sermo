package config

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/checks"
)

var wholeVarRef = regexp.MustCompile(`^\$\{\s*([^}:][^}]*)\s*\}$`)

// prepareExpansionInputs mutates the raw merged tree before variable collection:
// resource preflight entries narrow candidate lists to the path valid for their
// type, and command exports declare variables with their configured defaults.
// Command execution itself stays out of config resolution.
func prepareExpansionInputs(tree map[string]any) []string {
	var errs []string
	errs = append(errs, resolvePreflightResourceVariables(tree)...)
	applyCommandExportDefaults(tree)
	return errs
}

func resolvePreflightResourceVariables(tree map[string]any) []string {
	preflight, ok := tree[sectionPreflight].(map[string]any)
	if !ok {
		return nil
	}
	var errs []string
	for _, name := range slices.Sorted(maps.Keys(preflight)) {
		entry, ok := preflight[name].(map[string]any)
		if !ok {
			continue
		}
		typ := cfgval.String(entry[checks.CheckKeyType])
		if !isResourcePreflightType(typ) {
			continue
		}
		selected, _, ok := selectResourcePath(typ, entry[checks.CheckKeyPath])
		if !ok {
			continue
		}
		if selected != "" {
			entry[checks.CheckKeyPath] = selected
		}
		if ref, isRef := variablePathRef(entry[checks.CheckKeyPath]); isRef {
			vars := ensureVariables(tree)
			raw, exists := vars[ref]
			if !exists {
				continue
			}
			selected, _, ok = selectResourcePath(typ, raw)
			if !ok {
				continue
			}
			vars[ref] = selected
			entry[checks.CheckKeyPath] = selected
		}
	}
	return errs
}

func ensureVariables(tree map[string]any) map[string]any {
	vars, _ := tree[sectionVariables].(map[string]any)
	if vars == nil {
		vars = map[string]any{}
		tree[sectionVariables] = vars
	}
	return vars
}

func isResourcePreflightType(typ string) bool {
	switch typ {
	case checks.CheckTypeBinary, checks.CheckTypeFile, checks.CheckTypeSocket, checks.CheckTypePidfile, checks.CheckTypeLockfile:
		return true
	default:
		return false
	}
}

func variablePathRef(raw any) (string, bool) {
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	m := wholeVarRef.FindStringSubmatch(s)
	if m == nil {
		return "", false
	}
	name := strings.TrimSpace(m[1])
	if name == "" || strings.HasPrefix(name, "env:") {
		return "", false
	}
	return name, true
}

func selectResourcePath(typ string, raw any) (selected string, matched bool, ok bool) {
	candidates := cfgval.StringList(raw)
	if len(candidates) == 0 {
		return "", false, false
	}
	for _, candidate := range candidates {
		path := expandEnvString(candidate)
		if path == "" {
			continue
		}
		if selected == "" {
			selected = path
		}
		if resourceCandidateMatches(typ, path) {
			return path, true, true
		}
	}
	return selected, false, true
}

func resourceCandidateMatches(typ, path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	switch typ {
	case checks.CheckTypeBinary:
		return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
	case checks.CheckTypeFile, checks.CheckTypePidfile, checks.CheckTypeLockfile:
		return info.Mode().IsRegular()
	case checks.CheckTypeSocket:
		return info.Mode()&os.ModeSocket != 0
	default:
		return false
	}
}

func applyCommandExportDefaults(tree map[string]any) {
	for _, sectionName := range []string{sectionCommands, sectionPreflight} {
		section, ok := tree[sectionName].(map[string]any)
		if !ok {
			continue
		}
		applyCommandExportDefaultsFromSection(tree, section)
	}
}

func applyCommandExportDefaultsFromSection(tree map[string]any, section map[string]any) {
	for _, name := range slices.Sorted(maps.Keys(section)) {
		entry, ok := section[name].(map[string]any)
		if !ok || len(cfgval.StringArray(entry[checks.CheckKeyCommand])) == 0 {
			continue
		}
		switch name {
		case checks.DataKeyVersion:
			setExportDefault(tree, checks.DataKeyVersion, "")
			setExportDefault(tree, checks.DataKeyVersionShort, "")
		case checks.DataKeyVersionShort:
			setExportDefault(tree, checks.DataKeyVersionShort, "")
		}
		exports, ok := entry[checks.CheckKeyExport].(map[string]any)
		if !ok {
			continue
		}
		for _, varName := range slices.Sorted(maps.Keys(exports)) {
			setExportDefault(tree, varName, exportDefault(exports[varName]))
		}
	}
}

func exportDefault(raw any) string {
	m, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	if v, present := m[varKeyDefault]; present {
		return cfgval.String(v)
	}
	return ""
}

func setExportDefault(tree map[string]any, name, value string) {
	if name == "" {
		return
	}
	ensureVariables(tree)[name] = value
}
