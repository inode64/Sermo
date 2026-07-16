package config

import (
	"fmt"
	"maps"
	"os"
	"regexp"
	"sermo/internal/cfgval"
	"slices"
	"strings"
)

// varRef matches a single ${name} reference. There is no escape syntax in the
// `${` always begins a reference.
var varRef = regexp.MustCompile(`\$\{([^}]*)\}`)

// Keys of a from_file variable spec: the source file, an optional directive to
// scope the search, the capture pattern and a fallback default value.
const (
	varKeyFromFile  = "from_file"
	varKeyDirective = "directive"
	varKeyPattern   = "pattern"
	varKeyDefault   = "default"
)

const (
	runtimeVarDate           = "date"
	runtimeVarEvent          = "event"
	runtimeVarAction         = "action"
	runtimeVarRuleDuration   = "rule.duration"
	runtimeVarRuleWindow     = "rule.window"
	runtimeVarCheckName      = "check.name"
	runtimeVarCheckType      = "check.type"
	runtimeVarCheckMetric    = "check.metric"
	runtimeVarCheckScope     = "check.scope"
	runtimeVarCheckOp        = "check.op"
	runtimeVarCheckThreshold = "check.threshold"
	runtimeVarCheckValue     = "check.value"
	runtimeVarChangePath     = "change.path"
	runtimeVarChangeApp      = "change.app"
	runtimeVarChangeLibrary  = "change.library"
	runtimeVarChangeLevel    = "change.level"
	runtimeVarChangeOld      = "change.old_version"
	runtimeVarChangeNew      = "change.new_version"
)

const (
	varEnvPrefix      = "env:"
	varEnvDefaultSep  = ":-"
	varEnvRefOpenText = "${env:"
	varRefNameGroup   = 1
)

var fromFileVariableKeys = set(varKeyFromFile, varKeyDirective, varKeyPattern, varKeyDefault)

// collectVariables reads the merged `variables` section into a flat string map.
// Values are stringified (a YAML int like `port: 8080` becomes "8080"). A
// list-valued variable is treated as candidate paths and resolves to the first
// one that exists on the filesystem (see firstExistingPath). Resource preflight
// entries may have already narrowed a list to the candidate that matches their
// type (binary, file, socket or pidfile); collectVariables just consumes the
// resulting variables map.
func collectVariables(tree map[string]any) map[string]string {
	return collectVariablesForKind(tree, cfgval.String(tree[keyKind]))
}

func collectVariablesForKind(tree map[string]any, _ string) map[string]string {
	raw, ok := tree[sectionVariables].(map[string]any)
	vars := map[string]string{}
	if ok {
		vars = make(map[string]string, len(raw))
		for k, v := range raw {
			if list, ok := v.([]any); ok {
				vars[k] = expandEnvString(firstExistingPath(list))
				continue
			}
			// A map-valued variable that reads from a config file (from_file)
			// resolves to its `default` here; resolveFileVars overrides it once
			// the other variables it references (e.g. ${config}) are known.
			if m, ok := v.(map[string]any); ok {
				if _, isFile := m[varKeyFromFile]; isFile {
					vars[k] = expandEnvString(cfgval.String(m[varKeyDefault]))
					continue
				}
			}
			// Resolve ${env:...} in a variable value here (before nested-variable
			// validation) so a variable can hold a secret from the environment.
			vars[k] = expandEnvString(cfgval.String(v))
		}
	}
	return vars
}

// firstExistingPath resolves a list-valued variable to the first candidate path
// that exists on the filesystem, stopping at the first hit. This lets a catalog service
// list alternative locations for the same binary (e.g. /lib vs /usr/lib) and
// bind the variable to whichever is present, so the rest of the document can
// reference it via ${name}. If none exist, it falls back to the first candidate
// so the value stays well-formed and downstream preflight checks report it as
// missing rather than expanding to an empty string.
func firstExistingPath(candidates []any) string {
	return firstExistingStringPath(cfgval.StringList(candidates))
}

func firstExistingStringPath(candidates []string) string {
	var first string
	for _, p := range candidates {
		p = expandEnvString(p)
		if p == "" {
			continue
		}
		if first == "" {
			first = p // first non-empty candidate, the fallback when none exist
		}
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return first
}

// DocumentBinary returns the document's configured binary variable. It is used
// by catalog inspection and version discovery paths that need to read raw catalog
// documents before full service resolution.
func DocumentBinary(tree map[string]any) string {
	return firstExistingStringPath(documentBinaryCandidates(tree))
}

func documentBinaryCandidates(tree map[string]any) []string {
	vars, _ := tree[sectionVariables].(map[string]any)
	if vars == nil {
		return nil
	}
	return cfgval.StringList(vars[VariableKeyBinary])
}

// resolveFileVars overrides each from_file variable with the value read from its
// config file. It runs after the rest of the variable map (and builtins) is
// assembled so the file path may reference other variables such as ${config}. A
// missing file or unmatched key leaves the default already set by
// collectVariablesForKind in place. Malformed specs and unresolved path
// variables are configuration errors.
func resolveFileVars(vars map[string]string, tree map[string]any) []string {
	raw, ok := tree[sectionVariables].(map[string]any)
	if !ok {
		return nil
	}
	var errs []string
	for name, v := range raw {
		spec, ok := v.(map[string]any)
		if !ok {
			continue
		}
		from, ok := spec[varKeyFromFile]
		if !ok {
			continue
		}
		var specErrs []string
		validateFromFileSpec(variablePath(name), spec, func(format string, args ...any) {
			specErrs = append(specErrs, fmt.Sprintf(format, args...))
		})
		if len(specErrs) > 0 {
			errs = append(errs, specErrs...)
			continue
		}
		path, pathErrs := substituteVars(cfgval.String(from), vars, variableFieldPath(name, varKeyFromFile))
		errs = append(errs, pathErrs...)
		if len(pathErrs) > 0 {
			continue
		}
		resolvedSpec, specErrs := resolveFromFileSpecVars(name, spec, vars)
		errs = append(errs, specErrs...)
		if len(specErrs) > 0 {
			continue
		}
		val, found, err := extractFileValue(path, resolvedSpec)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", variablePath(name), err))
			continue
		}
		if found {
			vars[name] = val
		}
	}
	return errs
}

func resolveFromFileSpecVars(name string, spec map[string]any, vars map[string]string) (map[string]any, []string) {
	out := maps.Clone(spec)
	pat := cfgval.String(spec[varKeyPattern])
	if pat == "" {
		return out, nil
	}
	resolved, errs := substitutePatternVars(pat, vars, variableFieldPath(name, varKeyPattern))
	if len(errs) > 0 {
		return out, errs
	}
	out[varKeyPattern] = resolved
	return out, nil
}

// substituteVars replaces ${name} references in s using vars. Unknown
// references are errors because from_file paths are evaluated during config
// resolution, not at runtime.
func substituteVars(s string, vars map[string]string, path string) (string, []string) {
	return substituteVarsWith(s, vars, path, func(value string) string { return value })
}

func substitutePatternVars(s string, vars map[string]string, path string) (string, []string) {
	return substituteVarsWith(s, vars, path, regexp.QuoteMeta)
}

// substituteVarsWith resolves variable references and transforms each resolved
// value before insertion. Patterns quote values while file paths retain them
// literally; unresolved references always report the same config-path error.
func substituteVarsWith(s string, vars map[string]string, path string, transform func(string) string) (string, []string) {
	var errs []string
	out := varRef.ReplaceAllStringFunc(s, func(ref string) string {
		name := varRefName(ref)
		if rest, ok := strings.CutPrefix(name, varEnvPrefix); ok {
			return transform(resolveEnvRef(rest))
		}
		if val, ok := vars[name]; ok {
			return transform(val)
		}
		errs = append(errs, fmt.Sprintf("variable ${%s} used in %s but not defined", name, path))
		return ref
	})
	return out, errs
}

// validateVariableValues rejects variable values that themselves contain
// ${...} (no nested variables).
func validateVariableValues(vars map[string]string) []string {
	var errs []string
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		if varRef.MatchString(vars[name]) {
			errs = append(errs, fmt.Sprintf("variable %s references another variable in its value %q (nested variables are not allowed)", name, vars[name]))
		}
	}
	return errs
}

func validateFromFileVariables(raw any, add addFunc) {
	vars, ok := raw.(map[string]any)
	if !ok {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(vars)) {
		spec, ok := vars[name].(map[string]any)
		if !ok {
			continue
		}
		if _, has := spec[varKeyFromFile]; !has {
			continue
		}
		validateFromFileSpec(variablePath(name), spec, add)
	}
}

func validateFromFileSpec(path string, spec map[string]any, add addFunc) {
	for _, key := range slices.Sorted(maps.Keys(spec)) {
		if _, ok := fromFileVariableKeys[key]; !ok {
			add("%s.%s is not supported; from_file variables accept from_file, directive, pattern and default", path, key)
		}
	}
	if cfgval.String(spec[varKeyFromFile]) == "" {
		add("%s.from_file is required", path)
	}
	if _, has := spec[varKeyDefault]; !has {
		add("%s.default is required", path)
	}
	readers := 0
	if _, has := spec[varKeyDirective]; has {
		readers++
		if cfgval.String(spec[varKeyDirective]) == "" {
			add("%s.directive must be non-empty", path)
		}
	}
	if _, has := spec[varKeyPattern]; has {
		readers++
		pat := cfgval.String(spec[varKeyPattern])
		patForValidation := patternWithVariablePlaceholders(pat)
		switch {
		case pat == "":
			add("%s.pattern must be non-empty", path)
		default:
			re, err := regexp.Compile(patForValidation)
			if err != nil {
				add("%s.pattern is not a valid regex: %v", path, err)
			} else if re.NumSubexp() < 1 {
				add("%s.pattern must define at least one capture group", path)
			}
		}
	}
	if readers != 1 {
		add("%s must define exactly one of directive or pattern", path)
	}
}

func patternWithVariablePlaceholders(pat string) string {
	return varRef.ReplaceAllString(pat, "sample")
}

// expandTree substitutes ${var} references across every string in the tree,
// once, leaving the `variables` section itself untouched. It returns the
// expanded tree and a list of errors for undefined references, each naming the
// dotted path where the reference appeared.
func expandTree(tree map[string]any, vars map[string]string) (map[string]any, []string) {
	var errs []string
	out := make(map[string]any, len(tree))
	for k, v := range tree {
		if k == sectionVariables {
			out[k] = v
			continue
		}
		out[k] = expandValue(v, vars, k, &errs)
	}
	return out, errs
}

func expandValue(v any, vars map[string]string, path string, errs *[]string) any {
	switch t := v.(type) {
	case string:
		return expandString(t, vars, path, errs)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = expandValue(e, vars, path+"."+k, errs)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = expandValue(e, vars, fmt.Sprintf("%s[%d]", path, i), errs)
		}
		return out
	default:
		return t
	}
}

// runtimeVars are substituted by the worker when an event is emitted, not during
// resolution. expandString leaves them as literal ${...} (without erroring) so a
// rule message can reference the firing event's context.
var runtimeVars = map[string]bool{
	runtimeVarDate:           true,
	runtimeVarEvent:          true,
	runtimeVarAction:         true,
	runtimeVarRuleDuration:   true,
	runtimeVarRuleWindow:     true,
	runtimeVarCheckName:      true,
	runtimeVarCheckType:      true,
	runtimeVarCheckMetric:    true,
	runtimeVarCheckScope:     true,
	runtimeVarCheckOp:        true,
	runtimeVarCheckThreshold: true,
	runtimeVarCheckValue:     true,
	runtimeVarChangePath:     true,
	runtimeVarChangeApp:      true,
	runtimeVarChangeLibrary:  true,
	runtimeVarChangeLevel:    true,
	runtimeVarChangeOld:      true,
	runtimeVarChangeNew:      true,
}

func expandString(s string, vars map[string]string, path string, errs *[]string) string {
	return varRef.ReplaceAllStringFunc(s, func(match string) string {
		name := varRefName(match)
		if rest, ok := strings.CutPrefix(name, varEnvPrefix); ok {
			return resolveEnvRef(rest) // ${env:NAME} -> environment, never an error
		}
		if val, ok := vars[name]; ok {
			return val
		}
		if runtimeVars[name] {
			return match // resolved at emit time by the worker
		}
		if isCheckSummaryPath(path) {
			return match // resolved from the check result after the probe runs
		}
		*errs = append(*errs, fmt.Sprintf("variable ${%s} used in %s but not defined", name, path))
		return match
	})
}

// isCheckSummaryPath permits check-result placeholders in a summary. Normal
// variables have already been resolved above, so this only defers names such as
// ${value}, ${older_than}, ${check.value}, and ${result.count} until a check has
// produced its runtime data.
func isCheckSummaryPath(path string) bool {
	return strings.HasSuffix(path, ".summary")
}

// resolveEnvRef resolves the body of an ${env:...} reference (the text after
// "env:") from the process environment, with an optional shell-style default:
// ${env:NAME} or ${env:NAME:-fallback}. An unset or empty variable yields the
// default ("" when none), never an error — secrets need not be present when the
// config is merely validated.
func resolveEnvRef(ref string) string {
	name, def := ref, ""
	if before, after, ok := strings.Cut(ref, varEnvDefaultSep); ok {
		name, def = before, after
	}
	if v := os.Getenv(strings.TrimSpace(name)); v != "" {
		return v
	}
	return def
}

// expandEnvString replaces only ${env:...} references in s, leaving every other
// ${...} untouched. Used to resolve secrets in the global config (which has no
// per-service variables) and inside variable values.
func expandEnvString(s string) string {
	if !strings.Contains(s, varEnvRefOpenText) {
		return s
	}
	return varRef.ReplaceAllStringFunc(s, func(match string) string {
		name := varRefName(match)
		if rest, ok := strings.CutPrefix(name, varEnvPrefix); ok {
			return resolveEnvRef(rest)
		}
		return match
	})
}

func varRefName(ref string) string {
	return strings.TrimSpace(varRef.FindStringSubmatch(ref)[varRefNameGroup])
}

// expandEnvTree resolves ${env:...} references across every string in a tree,
// recursively and in place, returning the same tree. Used for the global config.
func expandEnvTree(v any) any {
	switch t := v.(type) {
	case string:
		return expandEnvString(t)
	case map[string]any:
		for k, e := range t {
			t[k] = expandEnvTree(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = expandEnvTree(e)
		}
		return t
	default:
		return t
	}
}
