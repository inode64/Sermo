package config

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"

	"sermo/internal/cfgval"
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
		typ := cfgval.String(entry["type"])
		if !isResourcePreflightType(typ) {
			continue
		}
		selected, _, ok := selectResourcePath(typ, entry["path"])
		if !ok {
			continue
		}
		if selected != "" {
			entry["path"] = selected
		}
		if ref, isRef := variablePathRef(entry["path"]); isRef {
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
			entry["path"] = selected
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
	case "binary", "file", "socket", "pidfile", "lockfile":
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
	case "binary":
		return info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0
	case "file", "pidfile", "lockfile":
		return info.Mode().IsRegular()
	case "socket":
		return info.Mode()&os.ModeSocket != 0
	default:
		return false
	}
}

func applyCommandExportDefaults(tree map[string]any) {
	for _, sectionName := range []string{"commands", sectionPreflight} {
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
		if !ok || len(cfgval.StringArray(entry["command"])) == 0 {
			continue
		}
		switch name {
		case "version":
			setExportDefault(tree, "version", "")
			setExportDefault(tree, "version_short", "")
		case "version_short":
			setExportDefault(tree, "version_short", "")
		}
		exports, ok := entry["export"].(map[string]any)
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
	if v, present := m["default"]; present {
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
