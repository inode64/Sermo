package config

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// versionPlaceholder marks a profile name (and its filename) as a version
// template: `php-fpm-%v` expands to `php-fpm-8.3`, `php-fpm-7.4`, ... — one
// concrete profile per installed version.
const versionPlaceholder = "%v"

// versionVar is the variable a template uses inside paths so the discovered
// version can be substituted, e.g. binary: "/usr/lib64/php${version}/bin/php-fpm".
const versionVar = "version"

// materializeVersionTemplates replaces every version-template profile with one
// concrete profile per installed version. Multiple versions of the same
// application can be installed at once, so a single `name: foo-%v` profile yields
// `foo-1.2`, `foo-3.4`, ... — each discovered by globbing the template's `binary`
// path with `${version}` wildcarded. The template itself is dropped; if no
// version is installed it simply yields nothing. A template may `uses` a base
// profile (e.g. php-fpm-%v uses php-fpm) to inherit its checks, rules and
// processes; only the version-specific binary differs.
func (c *Config) materializeVersionTemplates() {
	var templates []*Document
	for _, name := range c.ProfileNames {
		if strings.Contains(name, versionPlaceholder) {
			if doc, ok := c.Profiles[name]; ok {
				templates = append(templates, doc)
			}
		}
	}
	for _, tmpl := range templates {
		body := c.templateBody(tmpl)
		for _, version := range discoverVersions(versionDiscoverySource(body)) {
			c.add(instantiateVersion(body, tmpl.Name, version, tmpl.Path))
		}
		c.dropProfile(tmpl.Name)
	}
}

// versionDiscoverySource returns the ${version}-bearing filesystem path Sermo
// globs to find installed versions. It is `versions.from` when set, otherwise the
// `binary` variable. Decoupling them lets a template monitor a generic binary
// (e.g. /usr/sbin/php-fpm) while discovering versions from a slot-specific path.
func versionDiscoverySource(body map[string]any) string {
	if v, ok := body["versions"].(map[string]any); ok {
		if from := scalarString(v["from"]); from != "" {
			return from
		}
	}
	return profileBinary(body)
}

// templateBody returns the template's body folded onto its `uses` base (if any),
// with the resolution-control keys stripped. The `${version}` references are left
// intact for instantiateVersion to bind.
func (c *Config) templateBody(tmpl *Document) map[string]any {
	body := stripMeta(tmpl.Body)
	body["kind"] = kindProfile
	if base := scalarString(tmpl.Body["uses"]); base != "" {
		if src, ok := c.Profiles[base]; ok {
			body = mergeMaps(stripMeta(src.Body), body)
			body["kind"] = kindProfile
		}
	}
	return body
}

// profileBinary returns the raw (unexpanded) `binary` variable of a profile body.
func profileBinary(body map[string]any) string {
	if vars, ok := body["variables"].(map[string]any); ok {
		return scalarString(vars["binary"])
	}
	return ""
}

// discoverVersions globs the binary template with `${version}` replaced by a
// filesystem wildcard and extracts the version that filled it from each match.
// Versions are de-duplicated and sorted for stable ordering.
func discoverVersions(binaryTmpl string) []string {
	marker := "${" + versionVar + "}"
	if !strings.Contains(binaryTmpl, marker) {
		return nil
	}
	matches, err := filepath.Glob(strings.ReplaceAll(binaryTmpl, marker, "*"))
	if err != nil {
		return nil
	}
	// A version starts with a digit and never spans a path separator. Anchoring on
	// a leading digit keeps an unbounded trailing placeholder (e.g.
	// /usr/sbin/php-fpm${version}) from mistaking siblings like php-fpm.conf or a
	// bare php-fpm symlink for a version.
	re := regexp.MustCompile("^" + strings.ReplaceAll(regexp.QuoteMeta(binaryTmpl), regexp.QuoteMeta(marker), "([0-9][^/]*)") + "$")
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

// instantiateVersion bakes a concrete version into a copy of the template body:
// `%v` in the name becomes the version, and every `${version}` reference in the
// body (binary path, display_name, ...) is substituted. Other `${var}` references
// are left for normal resolution.
func instantiateVersion(body map[string]any, templateName, version, path string) *Document {
	name := strings.ReplaceAll(templateName, versionPlaceholder, version)
	out := bindVersion(cloneMap(body), version).(map[string]any)
	out["kind"] = kindProfile
	out["name"] = name
	delete(out, "versions") // discovery metadata, not part of the concrete profile
	return &Document{Kind: kindProfile, Name: name, Path: path, Body: out}
}

// bindVersion replaces every "${version}" in every string of the tree with the
// concrete version. Unlike full expansion it touches only the version marker.
func bindVersion(v any, version string) any {
	marker := "${" + versionVar + "}"
	switch t := v.(type) {
	case string:
		return strings.ReplaceAll(t, marker, version)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = bindVersion(e, version)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = bindVersion(e, version)
		}
		return out
	default:
		return t
	}
}

// dropProfile removes a profile (and its document) from the config, used to
// retire a version template once its instances are registered.
func (c *Config) dropProfile(name string) {
	delete(c.Profiles, name)
	kept := make([]string, 0, len(c.ProfileNames))
	for _, n := range c.ProfileNames {
		if n != name {
			kept = append(kept, n)
		}
	}
	c.ProfileNames = kept
	docs := make([]*Document, 0, len(c.docs))
	for _, d := range c.docs {
		if d.Kind == kindProfile && d.Name == name {
			continue
		}
		docs = append(docs, d)
	}
	c.docs = docs
}
