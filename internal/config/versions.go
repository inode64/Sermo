package config

import (
	"os"
	"path/filepath"
	"regexp"
	"sermo/internal/cfgval"
	"sort"
	"strconv"
	"strings"
)

// tmplToken is a version-template placeholder. A daemon name carrying one (e.g.
// `php-fpm%v`, `python%n`) is a template: it materializes into one concrete
// daemon per discovered value. `placeholder` is replaced in the name; the body
// uses `${variable}`; `capture` is the regex that extracts a value from a globbed
// path, so different tokens accept different value shapes.
type tmplToken struct {
	placeholder string // in the name, e.g. "%v"
	variable    string // in the body, e.g. "version" → ${version}
	capture     string // regex for the value, e.g. "[0-9][^/]*"
	allowEmpty  bool   // whether the marker-less binary materializes an active-slot instance
}

func (t tmplToken) marker() string { return "${" + t.variable + "}" }

// tmplTokens are the supported placeholders. `%v` is a free-form version
// (`8.3`, `12.0.2`); `%n` is a plain integer (`2`, `3`). Discovered values must
// start with a digit, and `%n` rejects anything past the digits so `python%n`
// matches `python3` but not `python3.11`; `%v` and `%n` may additionally
// materialize one empty active-slot value when the marker-less binary exists.
var tmplTokens = []tmplToken{
	{placeholder: "%v", variable: "version", capture: "[0-9][^/]*", allowEmpty: true},
	{placeholder: "%n", variable: "n", capture: "[0-9]+", allowEmpty: true},
	{placeholder: "%s", variable: "sep", capture: "[-_]?"},
	{placeholder: "%i", variable: "instance", capture: "(?:[A-Za-z0-9][A-Za-z0-9_.-]*)?"},
}

const (
	templateCurrentMarker = "${current}"
	templateCurrentLabel  = "current"
)

// tokenFor returns the template token a name carries, or nil if it is not a
// version template.
func tokenFor(name string) *tmplToken {
	for i := range tmplTokens {
		if strings.Contains(name, tmplTokens[i].placeholder) {
			return &tmplTokens[i]
		}
	}
	return nil
}

// tokensFor returns every template token a name carries, in left-to-right order
// of appearance (e.g. tomcat-%v%s%i → [%v, %s, %i]). A name may combine a
// version (%v/%n), an optional separator (%s) and an instance (%i) so that one
// template materializes a structured identity such as tomcat-8.5-main.
func tokensFor(name string) []tmplToken {
	var out []tmplToken
	for i := 0; i < len(name); {
		if name[i] == '%' {
			matched := false
			for _, t := range tmplTokens {
				if strings.HasPrefix(name[i:], t.placeholder) {
					out = append(out, t)
					i += len(t.placeholder)
					matched = true
					break
				}
			}
			if matched {
				continue
			}
		}
		i++
	}
	return out
}

// materializeVersionTemplates replaces every version-template document with one
// concrete document per installed value. Multiple versions of the same
// application can be installed at once, so a single `name: foo%v` (or `foo%n`)
// yields `foo1.2`, `foo3.4`, ... with the token's `${...}` wildcarded. Apps and
// libraries discover from their own `versions.from`/`binary`; daemons discover
// from an explicit token-bearing `versions.from` or a linked app template
// (`apps: ["php-fpm${version}"]`). `%v` and `%n` may also register an empty
// active-slot value when the marker-less app binary exists (e.g. `php%v` ->
// `php`), and any template can declare `versions.current_from` to materialize
// that active-slot entry explicitly. The template itself is dropped; if nothing
// is installed and no active slot is declared it yields nothing. A daemon
// template may `uses` a base daemon (e.g. php-fpm%v uses php-fpm) to inherit its
// checks, rules and processes.
func (c *Config) materializeVersionTemplates() {
	c.materializeRegistry(c.DaemonNames, c.Daemons, kindDaemon)
	c.materializeRegistry(c.AppNames, c.Apps, kindApp)
	c.materializeRegistry(c.LibraryNames, c.Libraries, kindLibrary)
}

// materializeRegistry materializes the version templates in one registry (the
// daemon/app/lib map), tagging each concrete instance with that kind so it is
// indexed in the same registry as its template.
func (c *Config) materializeRegistry(names []string, reg map[string]*Document, kind string) {
	var templates []*Document
	for _, name := range names {
		if tokenFor(name) != nil {
			if doc, ok := reg[name]; ok {
				templates = append(templates, doc)
			}
		}
	}
	for _, tmpl := range templates {
		c.recordTemplateValidationIssues(tmpl)
		body := c.templateBody(tmpl, kind)
		var instances []*Document
		toks := tokensFor(tmpl.Name)
		if len(toks) > 1 {
			instances = c.materializeMultiToken(tmpl, body, toks, kind)
		} else {
			tok := toks[0]
			source := c.versionDiscoverySource(body, tok, kind)
			matches := materializedTemplateMatches(source.paths, source.binary, source.options, toks)
			matches = c.withCurrentMatches(matches, tmpl.Name, toks, kind)
			for _, match := range matches {
				instances = append(instances, instantiateVersion(
					body, tmpl.Name, match, tok, tmpl.Path, kind,
				))
			}
		}
		for _, inst := range instances {
			if existing, ok := reg[inst.Name]; ok && existing.Name == inst.Name {
				c.recordMaterializedNameCollision(kind, tmpl, inst, existing)
				continue
			}
			inst.Category = tmpl.Category
			c.add(inst)
		}
		c.dropTemplate(tmpl.Name, reg, kind)
	}
}

func (c *Config) recordTemplateValidationIssues(tmpl *Document) {
	c.validationIssues = append(c.validationIssues, validateVersionsCurrentFrom(tmpl, documentScope(tmpl))...)
}

func (c *Config) recordMaterializedNameCollision(kind string, tmpl, inst, existing *Document) {
	c.materializedNameCollisions = append(c.materializedNameCollisions, materializedNameCollision{
		Kind:         kind,
		Name:         inst.Name,
		TemplateName: tmpl.Name,
		TemplatePath: tmpl.Path,
		ExistingPath: existing.Path,
	})
}

// materializeMultiToken materializes a template whose name carries more than one
// token (e.g. tomcat-%v%s%i). All markers are discovered together from a single
// glob whose matches yield one value per token; each present combination becomes
// a concrete document with every token bound in the name and body at once.
func (c *Config) materializeMultiToken(tmpl *Document, body map[string]any, toks []tmplToken, kind string) []*Document {
	source := c.multiTokenDiscoverySource(body, toks, kind)
	if len(source.paths) == 0 && len(versionsCurrentFromCandidates(body)) == 0 {
		return nil
	}
	require := versionsRequire(body)
	var out []*Document
	matches := materializedTemplateMatches(source.paths, source.binary, body, toks)
	matches = c.withCurrentMatches(matches, tmpl.Name, toks, kind)
	for _, match := range matches {
		if !requireSatisfied(require, match.values, toks) {
			continue
		}
		out = append(out, instantiateMulti(body, tmpl.Name, match, toks, tmpl.Path, kind))
	}
	return out
}

// versionsRequire returns the optional `versions.require` candidate paths. When
// set, a discovered instance materializes only if at least one of them exists on
// disk (with the captured tokens bound) — so a config directory whose runtime
// binary is not installed (e.g. /etc/php/fpm-php5.6 without php-fpm5.6) does not
// produce a daemon that would dangle a link to an unmaterialized binary app.
func versionsRequire(body map[string]any) []string {
	v, ok := body["versions"].(map[string]any)
	if !ok {
		return nil
	}
	return cfgval.StringList(v["require"])
}

func requireSatisfied(require []string, vals map[string]string, toks []tmplToken) bool {
	if len(require) == 0 {
		return true
	}
	pairs := make([]string, 0, len(toks)*2)
	for _, t := range toks {
		pairs = append(pairs, t.marker(), vals[t.variable])
	}
	repl := strings.NewReplacer(pairs...)
	for _, cand := range require {
		if _, err := os.Stat(repl.Replace(cand)); err == nil {
			return true
		}
	}
	return false
}

// multiTokenDiscoverySource returns the globs (carrying all markers) that
// enumerate a multi-token template's instances. Daemons discover only from an
// explicit `versions.from` or from a linked app template; apps and libraries can
// discover from `versions.from` or their own `variables.binary` candidates.
func (c *Config) multiTokenDiscoverySource(body map[string]any, toks []tmplToken, kind string) versionDiscovery {
	if paths := pathsContainingAllMarkers(versionsFromPaths(body), toks); len(paths) > 0 {
		return versionDiscovery{paths: paths, options: body}
	}
	if kind != kindDaemon {
		return versionDiscovery{
			paths:   pathsContainingAllMarkers(documentBinaryCandidates(body), toks),
			options: body,
			binary:  true,
		}
	}
	for _, name := range cfgval.StringList(body["apps"]) {
		doc, ok := c.Apps[linkedAppTemplateNameMulti(name, toks)]
		if !ok {
			continue
		}
		appBody := stripMeta(doc.Body)
		if paths := pathsContainingAllMarkers(versionsFromPaths(appBody), toks); len(paths) > 0 {
			return versionDiscovery{paths: paths, options: appBody}
		}
		if paths := pathsContainingAllMarkers(documentBinaryCandidates(appBody), toks); len(paths) > 0 {
			return versionDiscovery{paths: paths, options: appBody, binary: true}
		}
	}
	return versionDiscovery{options: body}
}

func pathsContainingAllMarkers(paths []string, toks []tmplToken) []string {
	var out []string
	for _, path := range paths {
		if containsAllMarkers(path, toks) {
			out = append(out, path)
		}
	}
	return out
}

func containsAllMarkers(path string, toks []tmplToken) bool {
	if path == "" {
		return false
	}
	for _, t := range toks {
		if t.variable == "sep" {
			continue
		}
		if !strings.Contains(path, t.marker()) {
			return false
		}
	}
	return true
}

func linkedAppTemplateNameMulti(name string, toks []tmplToken) string {
	pairs := make([]string, 0, len(toks)*2)
	for _, t := range toks {
		pairs = append(pairs, t.marker(), t.placeholder)
	}
	return strings.NewReplacer(pairs...).Replace(name)
}

// buildMultiRegex compiles an anchored regex matching the discovery path with one
// capture group per token, and returns the tokens in capture order. A non-final
// %v is bounded so it cannot swallow the separator before the instance.
func buildMultiRegex(path string, toks []tmplToken) (*regexp.Regexp, []tmplToken) {
	terminal := ""
	if len(toks) > 0 {
		terminal = toks[len(toks)-1].placeholder
	}
	var sb strings.Builder
	sb.WriteString("^")
	var order []tmplToken
	rest := path
	for len(rest) > 0 {
		idx, tk := earliestMarker(rest, toks)
		if tk == nil {
			sb.WriteString(regexp.QuoteMeta(rest))
			break
		}
		sb.WriteString(regexp.QuoteMeta(rest[:idx]))
		sb.WriteString("(" + captureFor(*tk, tk.placeholder == terminal) + ")")
		order = append(order, *tk)
		rest = rest[idx+len(tk.marker()):]
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return nil, nil
	}
	return re, order
}

// earliestMarker returns the index and token of the marker that appears first in
// s, or (-1, nil) when none is present.
func earliestMarker(s string, toks []tmplToken) (int, *tmplToken) {
	best := -1
	var bestTok *tmplToken
	for i := range toks {
		idx := strings.Index(s, toks[i].marker())
		if idx >= 0 && (best < 0 || idx < best) {
			best, bestTok = idx, &toks[i]
		}
	}
	return best, bestTok
}

// captureFor returns a token's capture regex. A non-terminal %v excludes the
// `-`/`_` separators so a structured name like tomcat-8.5-main splits cleanly.
func captureFor(tk tmplToken, terminal bool) string {
	if tk.placeholder == "%v" && !terminal {
		return "[0-9][^/_-]*"
	}
	return tk.capture
}

func tupleKey(toks []tmplToken, vals map[string]string) string {
	parts := make([]string, 0, len(toks))
	for _, t := range toks {
		parts = append(parts, vals[t.variable])
	}
	return strings.Join(parts, "\x00")
}

// instantiateMulti bakes one discovered tuple into a concrete document: each
// token placeholder in the name and each `${...}` marker in the body is bound to
// its captured value in a single pass.
func instantiateMulti(body map[string]any, templateName string, match templateMatch, toks []tmplToken, path, kind string) *Document {
	name := materializedTemplateName(templateName, match, toks)
	bodyPairs := make([]string, 0, len(toks)*2+2)
	for _, t := range toks {
		v := match.values[t.variable]
		bodyPairs = append(bodyPairs, t.marker(), v)
	}
	bodyPairs = append(bodyPairs, templateCurrentMarker, templateCurrentValue(match.current))
	out := bindTokens(cloneMap(body), strings.NewReplacer(bodyPairs...)).(map[string]any)
	if templateMatchHasEmptyValue(match, toks) {
		applyUnversionedOverrides(out)
	}
	injectMaterializedBinary(out, materializedBinaryFromMatch(body, kind, match))
	out["kind"] = kind
	out["name"] = name
	trimMaterializedMetadata(out)
	delete(out, "versions")
	return &Document{
		Kind:                 kind,
		Name:                 name,
		Path:                 path,
		Body:                 out,
		TemplateBaseName:     templateBaseName(templateName),
		TemplateCurrentLabel: templateUsesCurrentLabel(body),
	}
}

type templateMatch struct {
	values        map[string]string
	matchedPath   string
	realPath      string
	currentPaths  []string
	matchedBinary bool
	current       bool
}

func materializedTemplateMatches(discoverPaths []string, matchedBinary bool, options map[string]any, toks []tmplToken) []templateMatch {
	matches := discoverTokenMatches(discoverPaths, toks, matchedBinary)
	if tok, ok := unversionedTemplateToken(toks); ok && versionUnversionedEnabled(options, tok) {
		matches = append(matches, currentFromTemplateMatches(options, toks)...)
	}
	if len(toks) == 1 && versionUnversionedEnabled(options, toks[0]) {
		for _, discoverPath := range discoverPaths {
			if path, ok := unversionedVersionPath(discoverPath, toks[0]); ok {
				matches = append(matches, templateMatch{
					values:      map[string]string{toks[0].variable: ""},
					matchedPath: path,
					realPath:    realPathFor(path),
				})
			}
		}
	}
	matches = dedupeTemplateMatches(matches, toks)
	sortTemplateMatches(matches)
	return matches
}

func unversionedTemplateToken(toks []tmplToken) (tmplToken, bool) {
	for _, tok := range toks {
		if tok.allowEmpty {
			return tok, true
		}
	}
	return tmplToken{}, false
}

func currentFromTemplateMatches(body map[string]any, toks []tmplToken) []templateMatch {
	paths := versionsCurrentFromCandidates(body)
	if len(paths) == 0 {
		return nil
	}
	values := make(map[string]string, len(toks))
	for _, tok := range toks {
		values[tok.variable] = ""
	}
	path := paths[0]
	if path == "" {
		return nil
	}
	return []templateMatch{{
		values:       values,
		matchedPath:  path,
		realPath:     realPathFor(path),
		currentPaths: paths,
	}}
}

// discoverTokenMatches globs each discovery path with every marker wildcarded
// and keeps the matched path alongside the captured token values. When the
// discovery source is variables.binary, the matched path is the concrete runtime
// binary and should be baked into the materialized app/library.
func discoverTokenMatches(paths []string, toks []tmplToken, matchedBinary bool) []templateMatch {
	var out []templateMatch
	for _, path := range paths {
		if !containsAllMarkers(path, toks) {
			continue
		}
		pairs := make([]string, 0, len(toks)*2)
		for _, t := range toks {
			pairs = append(pairs, t.marker(), "*")
		}
		matches, err := filepath.Glob(strings.NewReplacer(pairs...).Replace(path))
		if err != nil {
			continue
		}
		re, order := buildMultiRegex(path, toks)
		if re == nil {
			continue
		}
		for _, matchPath := range matches {
			sub := re.FindStringSubmatch(matchPath)
			if sub == nil {
				continue
			}
			values := make(map[string]string, len(order))
			for i, tk := range order {
				values[tk.variable] = sub[i+1]
			}
			addImplicitTokenValues(values, toks)
			realPath := realPathFor(matchPath)
			values = refineMatchValues(values, path, realPath, toks)
			normalizeOptionalTupleValues(values)
			out = append(out, templateMatch{
				values:        values,
				matchedPath:   matchPath,
				realPath:      realPath,
				matchedBinary: matchedBinary,
			})
		}
	}
	return out
}

func addImplicitTokenValues(values map[string]string, toks []tmplToken) {
	for _, tok := range toks {
		if _, ok := values[tok.variable]; !ok && tok.variable == "sep" {
			values[tok.variable] = ""
		}
	}
}

func normalizeOptionalTupleValues(values map[string]string) {
	if values["instance"] == "" {
		values["sep"] = ""
	}
}

func refineMatchValues(values map[string]string, pattern, realPath string, toks []tmplToken) map[string]string {
	refined := refineMatchValuesFromRealPath(values, pattern, realPath, toks)
	return refineJavaReleaseVersion(refined, realPath)
}

func refineMatchValuesFromRealPath(values map[string]string, pattern, realPath string, toks []tmplToken) map[string]string {
	if realPath == "" {
		return values
	}
	patternParts := splitCleanPath(pattern)
	realParts := splitCleanPath(realPath)
	limit := len(patternParts)
	if len(realParts) < limit {
		limit = len(realParts)
	}
	for offset := 0; offset < limit; offset++ {
		patternPart := patternParts[len(patternParts)-1-offset]
		if !strings.Contains(patternPart, "${") {
			continue
		}
		realPart := realParts[len(realParts)-1-offset]
		re, order := buildMultiRegex(patternPart, toks)
		if re == nil {
			continue
		}
		sub := re.FindStringSubmatch(realPart)
		if sub == nil {
			continue
		}
		out := cloneStringMap(values)
		for i, tk := range order {
			out[tk.variable] = sub[i+1]
		}
		normalizeOptionalTupleValues(out)
		return out
	}
	return values
}

func splitCleanPath(path string) []string {
	return strings.Split(filepath.Clean(path), string(os.PathSeparator))
}

func refineJavaReleaseVersion(values map[string]string, realPath string) map[string]string {
	current := values["version"]
	if current == "" || filepath.Base(realPath) != "java" || filepath.Base(filepath.Dir(realPath)) != "bin" {
		return values
	}
	releasePath := filepath.Join(filepath.Dir(filepath.Dir(realPath)), "release")
	data, err := os.ReadFile(releasePath) //nolint:gosec // release is metadata under a discovered JVM home.
	if err != nil {
		return values
	}
	version := javaReleaseVersion(string(data))
	if !moreSpecificVersion(version, current) {
		return values
	}
	out := cloneStringMap(values)
	out["version"] = version
	return out
}

func javaReleaseVersion(data string) string {
	for _, line := range strings.Split(data, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok || key != "JAVA_VERSION" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

func moreSpecificVersion(candidate, current string) bool {
	if candidate == "" || candidate == current {
		return false
	}
	return strings.HasPrefix(candidate, current+".") || strings.HasPrefix(candidate, current+"_")
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func dedupeTemplateMatches(matches []templateMatch, toks []tmplToken) []templateMatch {
	seen := map[string]bool{}
	out := make([]templateMatch, 0, len(matches))
	for _, match := range matches {
		key := match.realPath
		if templateMatchHasEmptyValue(match, toks) {
			key = "unversioned:" + tupleKey(toks, match.values)
		} else if key == "" {
			key = match.matchedPath
		}
		if key == "" {
			key = tupleKey(toks, match.values)
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, match)
	}
	return out
}

func sortTemplateMatches(matches []templateMatch) {
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].values["version"] != matches[j].values["version"] {
			return versionLess(matches[i].values["version"], matches[j].values["version"])
		}
		if matches[i].values["n"] != matches[j].values["n"] {
			return versionLess(matches[i].values["n"], matches[j].values["n"])
		}
		if matches[i].values["instance"] != matches[j].values["instance"] {
			return matches[i].values["instance"] < matches[j].values["instance"]
		}
		return matches[i].matchedPath < matches[j].matchedPath
	})
}

func realPathFor(path string) string {
	realPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	return realPath
}

func (c *Config) withCurrentMatches(matches []templateMatch, templateName string, toks []tmplToken, kind string) []templateMatch {
	candidates := currentPathsFromUnversionedMatches(matches, toks)
	candidates = append(candidates, c.templateCurrentCandidatePaths(templateName, kind)...)
	if len(candidates) == 0 {
		return matches
	}
	for i := range matches {
		if templateMatchHasEmptyValue(matches[i], toks) {
			continue
		}
		if templateMatchMatchesAnyPath(matches[i], candidates) {
			matches[i].current = true
		}
	}
	return matches
}

func currentPathsFromUnversionedMatches(matches []templateMatch, toks []tmplToken) []string {
	var paths []string
	for _, match := range matches {
		if !templateMatchHasEmptyValue(match, toks) || match.matchedPath == "" {
			continue
		}
		if len(match.currentPaths) > 0 {
			paths = append(paths, match.currentPaths...)
			continue
		}
		paths = append(paths, match.matchedPath)
	}
	return paths
}

func templateMatchHasEmptyValue(match templateMatch, toks []tmplToken) bool {
	for _, tok := range toks {
		if (tok.variable == "version" || tok.variable == "n") && match.values[tok.variable] == "" {
			return true
		}
	}
	return false
}

func (c *Config) templateCurrentCandidatePaths(templateName, kind string) []string {
	baseName := templateBaseName(templateName)
	if baseName == "" {
		return nil
	}
	var doc *Document
	switch kind {
	case kindApp:
		doc = c.Apps[baseName]
	case kindLibrary:
		doc = c.Libraries[baseName]
	case kindDaemon:
		doc = c.Daemons[baseName]
	}
	if doc == nil || doc.Name == templateName {
		return nil
	}
	path := DocumentBinary(doc.Body)
	if path == "" {
		return nil
	}
	return []string{path}
}

func templateBaseName(templateName string) string {
	first := len(templateName)
	for _, tok := range tmplTokens {
		if idx := strings.Index(templateName, tok.placeholder); idx >= 0 && idx < first {
			first = idx
		}
	}
	if first == len(templateName) {
		return ""
	}
	return strings.TrimRight(templateName[:first], "-_.")
}

func templateMatchMatchesAnyPath(match templateMatch, paths []string) bool {
	for _, path := range paths {
		if sameFile(path, match.matchedPath) || sameFile(path, match.realPath) {
			return true
		}
	}
	return false
}

func sameFile(a, b string) bool {
	ainfo, err := os.Stat(a)
	if err != nil {
		return false
	}
	binfo, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(ainfo, binfo)
}

// versionLess orders discovered version values numerically by their
// dot-separated segments, so `8.3` < `8.11` < `10.0` instead of the
// lexicographic `10.0` < `8.11` < `8.3` that `sort.Strings` would yield.
// Non-numeric segments (e.g. an `8.3-rc1` suffix) fall back to a string
// compare, and the empty active-slot value sorts first.
func versionLess(a, b string) bool {
	if a == "" || b == "" {
		return a == "" && b != ""
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		if aerr == nil && berr == nil {
			if an != bn {
				return an < bn
			}
			continue
		}
		if as[i] != bs[i] {
			return as[i] < bs[i]
		}
	}
	return len(as) < len(bs)
}

type versionDiscovery struct {
	paths   []string
	options map[string]any
	binary  bool
}

// versionDiscoverySource returns the placeholder-bearing filesystem path Sermo
// globs to find installed values, plus the document whose `versions.unversioned`
// option controls active-slot behavior. Apps and libraries own their discovery
// path directly. Daemons intentionally do not: they must link a matching app
// template, and that app owns the installed-version source.
func (c *Config) versionDiscoverySource(body map[string]any, tok tmplToken, kind string) versionDiscovery {
	if kind != kindDaemon {
		if paths := versionsFromPaths(body); len(paths) > 0 {
			return versionDiscovery{paths: paths, options: body}
		}
		return versionDiscovery{paths: documentBinaryCandidates(body), options: body, binary: true}
	}
	// A daemon may own its discovery via an explicit token-bearing
	// `versions.from`: instance configs live on the daemon, not on the version
	// binary the linked app knows about (e.g. /etc/tomcat-${version}${sep}
	// ${instance}/server.xml). Prefer it when present. A daemon still never
	// discovers from its own *binary* — that remains the linked app's job — so
	// only an explicit versions.from qualifies, not documentBinaryCandidates.
	if paths := pathsContainingMarker(versionsFromPaths(body), tok.marker()); len(paths) > 0 {
		return versionDiscovery{paths: paths, options: body}
	}
	for _, name := range cfgval.StringList(body["apps"]) {
		doc, ok := c.Apps[linkedAppTemplateName(name, tok)]
		if !ok {
			continue
		}
		appBody := stripMeta(doc.Body)
		if paths := versionsFromPaths(appBody); anyContains(paths, tok.marker()) {
			return versionDiscovery{paths: paths, options: appBody}
		}
		if paths := documentBinaryCandidates(appBody); anyContains(paths, tok.marker()) {
			return versionDiscovery{paths: paths, options: appBody, binary: true}
		}
	}
	return versionDiscovery{options: body}
}

func directVersionDiscoverySources(body map[string]any) []string {
	if from := versionsFromPaths(body); len(from) > 0 {
		return from
	}
	return documentBinaryCandidates(body)
}

func pathsContainingMarker(paths []string, marker string) []string {
	var out []string
	for _, path := range paths {
		if strings.Contains(path, marker) {
			out = append(out, path)
		}
	}
	return out
}

// versionsFromPaths returns the explicit `versions.from` discovery globs.
func versionsFromPaths(body map[string]any) []string {
	if v, ok := body["versions"].(map[string]any); ok {
		return cfgval.StringList(v["from"])
	}
	return nil
}

func versionsCurrentFromCandidates(body map[string]any) []string {
	v, ok := body["versions"].(map[string]any)
	if !ok {
		return nil
	}
	return cfgval.StringList(v["current_from"])
}

func anyContains(values []string, marker string) bool {
	for _, value := range values {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func linkedAppTemplateName(name string, tok tmplToken) string {
	return strings.ReplaceAll(name, tok.marker(), tok.placeholder)
}

func versionUnversionedEnabled(body map[string]any, tok tmplToken) bool {
	versions, ok := body["versions"].(map[string]any)
	if !ok {
		return tok.allowEmpty
	}
	raw, present := versions["unversioned"]
	if !present {
		return tok.allowEmpty
	}
	if b, ok := raw.(bool); ok {
		return b
	}
	opts, ok := raw.(map[string]any)
	if !ok {
		return false
	}
	if enabled, present := opts["enabled"]; present {
		return cfgval.Bool(enabled)
	}
	return true
}

func unversionedVersionPath(discoverPath string, tok tmplToken) (string, bool) {
	marker := tok.marker()
	if !strings.Contains(discoverPath, marker) {
		return "", false
	}
	path := strings.ReplaceAll(discoverPath, marker, "")
	_, err := os.Stat(path)
	return path, err == nil
}

// templateBody returns the template's body folded onto its `uses` base (if any),
// with the resolution-control keys stripped. The `${...}` references are left
// intact for instantiateVersion to bind.
func (c *Config) templateBody(tmpl *Document, kind string) map[string]any {
	body := stripMeta(tmpl.Body)
	body["kind"] = kind
	if base := cfgval.String(tmpl.Body["uses"]); base != "" {
		if src, ok := c.Daemons[base]; ok {
			body = mergeMaps(stripMeta(src.Body), body)
			body["kind"] = kind
		}
	}
	return body
}

// discoverVersions globs the discovery path with the token's `${...}` replaced by
// a filesystem wildcard and extracts the value that filled it from each match.
// Values are de-duplicated and sorted for stable ordering.
func discoverVersions(discoverPath string, tok tmplToken) []string {
	marker := tok.marker()
	if !strings.Contains(discoverPath, marker) {
		return nil
	}
	matches, err := filepath.Glob(strings.ReplaceAll(discoverPath, marker, "*"))
	if err != nil {
		return nil
	}
	// The captured value never spans a path separator. Its shape comes from the
	// token (`capture`), which keeps an unbounded trailing placeholder (e.g.
	// /usr/sbin/php-fpm${version}) from mistaking siblings like php-fpm.conf or a
	// bare symlink for a value.
	re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(discoverPath), regexp.QuoteMeta(marker), "("+tok.capture+")") + "$")
	seen := map[string]bool{}
	var out []string
	for _, m := range matches {
		sub := re.FindStringSubmatch(m)
		if sub == nil {
			continue
		}
		if v := sub[1]; !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// instantiateVersion bakes a concrete value into a copy of the template body: the
// token placeholder in the name becomes the value, and every `${...}` reference
// for that token in the body (variables.binary, display_name, service, ...) is
// substituted. Other `${var}` references are left for normal resolution.
func instantiateVersion(body map[string]any, templateName string, match templateMatch, tok tmplToken, path, kind string) *Document {
	value := match.values[tok.variable]
	name := materializedTemplateName(templateName, match, []tmplToken{tok})
	out := bindTokens(cloneMap(body), strings.NewReplacer(tok.marker(), value, templateCurrentMarker, templateCurrentValue(match.current))).(map[string]any)
	if value == "" {
		applyUnversionedOverrides(out)
	}
	injectMaterializedBinary(out, materializedBinaryFromMatch(body, kind, match))
	out["kind"] = kind
	out["name"] = name
	trimMaterializedMetadata(out)
	delete(out, "versions") // discovery metadata, not part of the concrete definition
	return &Document{
		Kind:                 kind,
		Name:                 name,
		Path:                 path,
		Body:                 out,
		TemplateBaseName:     templateBaseName(templateName),
		TemplateCurrentLabel: templateUsesCurrentLabel(body),
	}
}

func templateUsesCurrentLabel(body map[string]any) bool {
	for _, key := range []string{"display_name", "description"} {
		if strings.Contains(cfgval.String(body[key]), templateCurrentMarker) {
			return true
		}
	}
	return false
}

func materializedTemplateName(templateName string, match templateMatch, toks []tmplToken) string {
	if templateMatchHasEmptyValue(match, toks) {
		if base := templateBaseName(templateName); base != "" {
			return base
		}
	}
	name := templateName
	for _, tok := range toks {
		name = strings.ReplaceAll(name, tok.placeholder, match.values[tok.variable])
	}
	return strings.TrimSpace(name)
}

func templateCurrentValue(current bool) string {
	if current {
		return templateCurrentLabel
	}
	return ""
}

func materializedBinaryFromMatch(body map[string]any, kind string, match templateMatch) string {
	if kind != kindApp && kind != kindLibrary {
		return ""
	}
	if len(match.currentPaths) > 0 {
		return match.matchedPath
	}
	if match.matchedPath == "" {
		return ""
	}
	if match.matchedBinary {
		return match.matchedPath
	}
	if len(documentBinaryCandidates(body)) > 0 {
		return ""
	}
	return match.matchedPath
}

func injectMaterializedBinary(out map[string]any, binary string) {
	if binary == "" {
		return
	}
	vars, _ := out["variables"].(map[string]any)
	if vars == nil {
		vars = map[string]any{}
		out["variables"] = vars
	}
	vars["binary"] = binary
}

func trimMaterializedMetadata(out map[string]any) {
	for _, key := range []string{"name", "display_name", "description"} {
		if value, ok := out[key].(string); ok {
			out[key] = strings.TrimSpace(value)
		}
	}
}

func applyUnversionedOverrides(out map[string]any) {
	versions, ok := out["versions"].(map[string]any)
	if !ok {
		return
	}
	overrides, ok := versions["unversioned"].(map[string]any)
	if !ok {
		return
	}
	for key, value := range overrides {
		if key == "enabled" {
			continue
		}
		out[key] = value
	}
}

// bindTokens applies a Replacer to every string of the tree in one pass,
// cloning maps/lists as it goes — so multiple built-in tokens (arch, os) cost
// one tree walk instead of one per token.
func bindTokens(v any, repl *strings.Replacer) any {
	switch t := v.(type) {
	case string:
		return repl.Replace(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = bindTokens(e, repl)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = bindTokens(e, repl)
		}
		return out
	default:
		return t
	}
}

// dropTemplate removes a version template (and its document) from its registry
// once its concrete instances are registered.
func (c *Config) dropTemplate(name string, reg map[string]*Document, kind string) {
	delete(reg, name)
	switch kind {
	case kindDaemon:
		c.DaemonNames = withoutString(c.DaemonNames, name)
	case kindApp:
		c.AppNames = withoutString(c.AppNames, name)
	case kindLibrary:
		c.LibraryNames = withoutString(c.LibraryNames, name)
	}
	docs := make([]*Document, 0, len(c.docs))
	for _, d := range c.docs {
		if d.Kind == kind && d.Name == name {
			continue
		}
		docs = append(docs, d)
	}
	c.docs = docs
}

// withoutString returns names with every occurrence of name removed.
func withoutString(names []string, name string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}
