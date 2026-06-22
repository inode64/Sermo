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
// `php`). The template itself is dropped; if nothing is installed it yields
// nothing. A daemon template may `uses` a base daemon (e.g. php-fpm%v uses
// php-fpm) to inherit its checks, rules and processes.
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
		body := c.templateBody(tmpl, kind)
		var instances []*Document
		if toks := tokensFor(tmpl.Name); len(toks) > 1 {
			instances = c.materializeMultiToken(tmpl, body, toks, kind)
		} else {
			tok := tokenFor(tmpl.Name)
			source := c.versionDiscoverySource(body, *tok, kind)
			values := materializedVersionValues(source.paths, source.options, *tok)
			for _, value := range values {
				instances = append(instances, instantiateVersion(body, tmpl.Name, value, *tok, tmpl.Path, kind))
			}
		}
		for _, inst := range instances {
			if existing, ok := reg[inst.Name]; ok && existing.Name == inst.Name {
				continue
			}
			inst.Category = tmpl.Category
			c.add(inst)
		}
		c.dropTemplate(tmpl.Name, reg, kind)
	}
}

// materializeMultiToken materializes a template whose name carries more than one
// token (e.g. tomcat-%v%s%i). All markers are discovered together from a single
// glob whose matches yield one value per token; each present combination becomes
// a concrete document with every token bound in the name and body at once.
func (c *Config) materializeMultiToken(tmpl *Document, body map[string]any, toks []tmplToken, kind string) []*Document {
	path := c.multiTokenDiscoveryPath(body, toks, kind)
	if path == "" {
		return nil
	}
	require := versionsRequire(body)
	var out []*Document
	for _, vals := range discoverTokenTuples(path, toks) {
		if !requireSatisfied(require, vals, toks) {
			continue
		}
		out = append(out, instantiateMulti(body, tmpl.Name, vals, toks, tmpl.Path, kind))
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

// multiTokenDiscoveryPath returns the glob (carrying all markers) that enumerates
// a multi-token template's instances: the daemon's own `versions.from` when it
// carries every marker, else a linked app template that does.
func (c *Config) multiTokenDiscoveryPath(body map[string]any, toks []tmplToken, kind string) string {
	if from := versionsFromPath(body); containsAllMarkers(from, toks) {
		return from
	}
	if kind != kindDaemon {
		return ""
	}
	for _, name := range cfgval.StringList(body["apps"]) {
		doc, ok := c.Apps[linkedAppTemplateNameMulti(name, toks)]
		if !ok {
			continue
		}
		if from := versionsFromPath(stripMeta(doc.Body)); containsAllMarkers(from, toks) {
			return from
		}
	}
	return ""
}

func containsAllMarkers(path string, toks []tmplToken) bool {
	if path == "" {
		return false
	}
	for _, t := range toks {
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

// discoverTokenTuples globs the discovery path with every marker wildcarded and,
// for each match, captures one value per token via a regex with a group per
// token. An empty trailing instance drops its separator so no dangling `-`/`_`
// survives. Tuples are de-duplicated and ordered by version then instance.
func discoverTokenTuples(path string, toks []tmplToken) []map[string]string {
	pairs := make([]string, 0, len(toks)*2)
	for _, t := range toks {
		pairs = append(pairs, t.marker(), "*")
	}
	matches, err := filepath.Glob(strings.NewReplacer(pairs...).Replace(path))
	if err != nil {
		return nil
	}
	re, order := buildMultiRegex(path, toks)
	if re == nil {
		return nil
	}
	var out []map[string]string
	seen := map[string]bool{}
	for _, m := range matches {
		sub := re.FindStringSubmatch(m)
		if sub == nil {
			continue
		}
		vals := make(map[string]string, len(order))
		for i, tk := range order {
			vals[tk.variable] = sub[i+1]
		}
		if vals["instance"] == "" {
			vals["sep"] = ""
		}
		key := tupleKey(toks, vals)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, vals)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i]["version"] != out[j]["version"] {
			return versionLess(out[i]["version"], out[j]["version"])
		}
		return out[i]["instance"] < out[j]["instance"]
	})
	return out
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
func instantiateMulti(body map[string]any, templateName string, vals map[string]string, toks []tmplToken, path, kind string) *Document {
	name := templateName
	bodyPairs := make([]string, 0, len(toks)*2)
	for _, t := range toks {
		v := vals[t.variable]
		name = strings.ReplaceAll(name, t.placeholder, v)
		bodyPairs = append(bodyPairs, t.marker(), v)
	}
	name = strings.TrimSpace(name)
	out := bindTokens(cloneMap(body), strings.NewReplacer(bodyPairs...)).(map[string]any)
	out["kind"] = kind
	out["name"] = name
	trimMaterializedMetadata(out)
	delete(out, "versions")
	return &Document{Kind: kind, Name: name, Path: path, Body: out}
}

func materializedVersionValues(discoverPaths []string, options map[string]any, tok tmplToken) []string {
	seen := map[string]bool{}
	var values []string
	for _, discoverPath := range discoverPaths {
		for _, value := range discoverVersions(discoverPath, tok) {
			if !seen[value] {
				seen[value] = true
				values = append(values, value)
			}
		}
		if versionUnversionedEnabled(options, tok) && unversionedVersionExists(discoverPath, tok) && !seen[""] {
			seen[""] = true
			values = append(values, "")
		}
	}
	if len(values) > 0 {
		sort.Slice(values, func(i, j int) bool { return versionLess(values[i], values[j]) })
	}
	return values
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
}

// versionDiscoverySource returns the placeholder-bearing filesystem path Sermo
// globs to find installed values, plus the document whose `versions.unversioned`
// option controls active-slot behavior. Apps and libraries own their discovery
// path directly. Daemons intentionally do not: they must link a matching app
// template, and that app owns the installed-version source.
func (c *Config) versionDiscoverySource(body map[string]any, tok tmplToken, kind string) versionDiscovery {
	if kind != kindDaemon {
		return versionDiscovery{paths: directVersionDiscoverySources(body), options: body}
	}
	// A daemon may own its discovery via an explicit token-bearing
	// `versions.from`: instance configs live on the daemon, not on the version
	// binary the linked app knows about (e.g. /etc/tomcat-${version}${sep}
	// ${instance}/server.xml). Prefer it when present. A daemon still never
	// discovers from its own *binary* — that remains the linked app's job — so
	// only an explicit versions.from qualifies, not documentBinaryCandidates.
	if from := versionsFromPath(body); strings.Contains(from, tok.marker()) {
		return versionDiscovery{paths: []string{from}, options: body}
	}
	for _, name := range cfgval.StringList(body["apps"]) {
		doc, ok := c.Apps[linkedAppTemplateName(name, tok)]
		if !ok {
			continue
		}
		appBody := stripMeta(doc.Body)
		sources := directVersionDiscoverySources(appBody)
		if anyContains(sources, tok.marker()) {
			return versionDiscovery{paths: sources, options: appBody}
		}
	}
	return versionDiscovery{options: body}
}

func directVersionDiscoverySources(body map[string]any) []string {
	if from := versionsFromPath(body); from != "" {
		return []string{from}
	}
	return documentBinaryCandidates(body)
}

// versionsFromPath returns the explicit `versions.from` discovery glob, or "".
func versionsFromPath(body map[string]any) string {
	if v, ok := body["versions"].(map[string]any); ok {
		return cfgval.String(v["from"])
	}
	return ""
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

func unversionedVersionExists(discoverPath string, tok tmplToken) bool {
	marker := tok.marker()
	if !strings.Contains(discoverPath, marker) {
		return false
	}
	_, err := os.Stat(strings.ReplaceAll(discoverPath, marker, ""))
	return err == nil
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
func instantiateVersion(body map[string]any, templateName, value string, tok tmplToken, path, kind string) *Document {
	name := strings.TrimSpace(strings.ReplaceAll(templateName, tok.placeholder, value))
	out := bindToken(cloneMap(body), tok.marker(), value).(map[string]any)
	if value == "" {
		applyUnversionedOverrides(out)
	}
	out["kind"] = kind
	out["name"] = name
	trimMaterializedMetadata(out)
	delete(out, "versions") // discovery metadata, not part of the concrete definition
	return &Document{Kind: kind, Name: name, Path: path, Body: out}
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

// bindToken replaces every occurrence of marker in every string of the tree with
// value. Unlike full expansion it touches only that one marker.
func bindToken(v any, marker, value string) any {
	return bindTokens(v, strings.NewReplacer(marker, value))
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
