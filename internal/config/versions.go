package config

import (
	"path/filepath"
	"regexp"
	"sermo/internal/cfgval"
	"sort"
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
}

func (t tmplToken) marker() string { return "${" + t.variable + "}" }

// tmplTokens are the supported placeholders. `%v` is a free-form version
// (`8.3`, `12.0.2`); `%n` is a plain integer (`2`, `3`) — both must start with a
// digit, but `%n` rejects anything past the digits so `python%n` matches
// `python3` but not `python3.11`.
var tmplTokens = []tmplToken{
	{placeholder: "%v", variable: "version", capture: "[0-9][^/]*"},
	{placeholder: "%n", variable: "n", capture: "[0-9]+"},
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

// materializeVersionTemplates replaces every version-template daemon with one
// concrete daemon per installed value. Multiple versions of the same
// application can be installed at once, so a single `name: foo%v` (or `foo%n`)
// daemon yields `foo1.2`, `foo3.4`, ... — each discovered by globbing the
// template's discovery path (`versions.from`, else `binary`) with the token's
// `${...}` wildcarded. The template itself is dropped; if nothing is installed it
// yields nothing. A template may `uses` a base daemon (e.g. php-fpm%v uses
// php-fpm) to inherit its checks, rules and processes; only the binary differs.
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
		tok := tokenFor(tmpl.Name)
		body := c.templateBody(tmpl, kind)
		for _, value := range discoverVersions(versionDiscoverySource(body), *tok) {
			inst := instantiateVersion(body, tmpl.Name, value, *tok, tmpl.Path, kind)
			inst.Category = tmpl.Category
			c.add(inst)
		}
		c.dropTemplate(tmpl.Name, reg, kind)
	}
}

// versionDiscoverySource returns the placeholder-bearing filesystem path Sermo
// globs to find installed values. It is `versions.from` when set, otherwise the
// `binary` variable. Decoupling them lets a template monitor a generic binary
// (e.g. /usr/sbin/php-fpm) while discovering from a slot-specific path.
func versionDiscoverySource(body map[string]any) string {
	if v, ok := body["versions"].(map[string]any); ok {
		if from := cfgval.String(v["from"]); from != "" {
			return from
		}
	}
	return daemonBinary(body)
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

// daemonBinary returns the raw (unexpanded) `binary` variable of a daemon body.
func daemonBinary(body map[string]any) string {
	if vars, ok := body["variables"].(map[string]any); ok {
		return cfgval.String(vars["binary"])
	}
	return ""
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
// for that token in the body (binary path, display_name, service, ...) is
// substituted. Other `${var}` references are left for normal resolution.
func instantiateVersion(body map[string]any, templateName, value string, tok tmplToken, path, kind string) *Document {
	name := strings.ReplaceAll(templateName, tok.placeholder, value)
	out := bindToken(cloneMap(body), tok.marker(), value).(map[string]any)
	out["kind"] = kind
	out["name"] = name
	delete(out, "versions") // discovery metadata, not part of the concrete definition
	return &Document{Kind: kind, Name: name, Path: path, Body: out}
}

// bindToken replaces every occurrence of marker in every string of the tree with
// value. Unlike full expansion it touches only that one marker.
func bindToken(v any, marker, value string) any {
	switch t := v.(type) {
	case string:
		return strings.ReplaceAll(t, marker, value)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = bindToken(e, marker, value)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = bindToken(e, marker, value)
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
