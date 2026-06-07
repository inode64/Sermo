package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// DefaultGlobalPath is the standard location of the global configuration.
const DefaultGlobalPath = "/etc/sermo/sermo.yml"

// Load reads the global configuration at globalPath and every profile and
// service document reachable from its `paths`. Parsing/IO failures abort; the
// returned Config carries documents in raw, unexpanded form for resolution.
func Load(globalPath string) (*Config, error) {
	global, err := loadGlobal(globalPath)
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		Global:   global,
		Profiles: map[string]*Document{},
		Services: map[string]*Document{},
	}

	profileDirs := global.Profiles
	if len(profileDirs) == 0 {
		profileDirs = []string{"/usr/share/sermo/profiles", "/etc/sermo/apps-available"}
	}
	enabledDirs := global.Enabled
	if len(enabledDirs) == 0 {
		enabledDirs = []string{"/etc/sermo/apps-enabled"}
	}

	for _, dir := range profileDirs {
		if err := cfg.loadDir(dir); err != nil {
			return nil, err
		}
	}
	for _, dir := range enabledDirs {
		if err := cfg.loadDir(dir); err != nil {
			return nil, err
		}
	}
	cfg.applyOSSelectors()
	cfg.bakeArch()
	cfg.bakeOS()
	cfg.materializeVersionTemplates()
	return cfg, nil
}

func loadGlobal(path string) (Global, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Global{}, fmt.Errorf("read global config %s: %w", path, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Global{}, fmt.Errorf("parse global config %s: %w", path, err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	// Resolve ${env:...} across the global config so secrets (notifier DSNs/
	// webhooks, web passwords, …) come from the environment, never the file.
	expandEnvTree(raw)

	g := Global{Path: path, Raw: raw}
	if defaults, ok := raw["defaults"].(map[string]any); ok {
		g.Defaults = defaults
	} else {
		g.Defaults = map[string]any{}
	}
	if paths, ok := raw["paths"].(map[string]any); ok {
		g.Profiles = stringList(paths["profiles"])
		g.Enabled = stringList(paths["enabled"])
		g.Runtime = scalarString(paths["runtime"])
		g.State = scalarString(paths["state"])
	}
	resolveConfigPaths(path, &g)
	return g, nil
}

// resolveConfigPaths makes profiles/enabled/runtime/state paths absolute. Relative
// entries are resolved against the global config file's directory so a tree like
// configs/sermo.yml with `enabled: [apps-enabled]` loads configs/apps-enabled
// when run from the repository.
func resolveConfigPaths(globalPath string, g *Global) {
	base := filepath.Dir(filepath.Clean(globalPath))
	g.Profiles = resolvePathList(base, g.Profiles)
	g.Enabled = resolvePathList(base, g.Enabled)
	if g.Runtime != "" {
		g.Runtime = resolveConfigPath(base, g.Runtime)
	}
	if g.State != "" {
		g.State = resolveConfigPath(base, g.State)
	}
}

func resolvePathList(base string, dirs []string) []string {
	if len(dirs) == 0 {
		return dirs
	}
	out := make([]string, len(dirs))
	for i, dir := range dirs {
		out[i] = resolveConfigPath(base, dir)
	}
	return out
}

func resolveConfigPath(base, p string) string {
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Clean(filepath.Join(base, p))
}

// loadDir reads every *.yml/*.yaml document in dir, recursing into
// subdirectories. A `services`/`apps`/`libs` subdirectory tags the profiles it
// holds with that category; files directly in dir default to CategoryService. A
// missing directory is not an error (a host may not have user profiles), but an
// unreadable one is.
func (c *Config) loadDir(dir string) error {
	return c.loadCategoryDir(dir, "")
}

func (c *Config) loadCategoryDir(dir, category string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read config dir %s: %w", dir, err)
	}

	names := make([]string, 0, len(entries))
	var subdirs []string
	for _, e := range entries {
		if e.IsDir() {
			subdirs = append(subdirs, e.Name())
			continue
		}
		if isYAML(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	sort.Strings(subdirs)

	for _, name := range names {
		doc, err := loadDocument(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		doc.Category = effectiveCategory(category)
		c.add(doc)
	}
	for _, name := range subdirs {
		sub := category
		if sub == "" {
			sub = categoryFromDir(name) // only the top level names a category
		}
		if err := c.loadCategoryDir(filepath.Join(dir, name), sub); err != nil {
			return err
		}
	}
	return nil
}

func effectiveCategory(category string) string {
	if category == "" {
		return CategoryService
	}
	return category
}

func loadDocument(path string) (*Document, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var body map[string]any
	if err := yaml.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if body == nil {
		body = map[string]any{}
	}
	return &Document{
		Kind: scalarString(body["kind"]),
		Name: scalarString(body["name"]),
		Path: path,
		Body: body,
	}, nil
}

// add indexes a document by name. The first document under each name wins for
// indexing; duplicate-name detection is reported by validation, which sees the
// later document's path.
func (c *Config) add(doc *Document) {
	switch doc.Kind {
	case kindProfile:
		if _, exists := c.Profiles[doc.Name]; !exists && doc.Name != "" {
			c.Profiles[doc.Name] = doc
		}
		c.ProfileNames = append(c.ProfileNames, doc.Name)
	case kindService:
		if _, exists := c.Services[doc.Name]; !exists && doc.Name != "" {
			c.Services[doc.Name] = doc
		}
		c.ServiceNames = append(c.ServiceNames, doc.Name)
	}
	c.docs = append(c.docs, doc)
}

// ProfilesInCategory returns the names of profiles in a category (service | app |
// library), sorted, for category-scoped listings such as `apps` and `libs`.
func (c *Config) ProfilesInCategory(category string) []string {
	var names []string
	for _, name := range c.ProfileNames {
		if doc, ok := c.Profiles[name]; ok && doc.Category == category {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// DisplayName returns the human-friendly `display_name` from a document body
// (e.g. "MariaDB"), falling back to fallback — typically the document's own
// `name` — when the field is absent or blank.
func DisplayName(body map[string]any, fallback string) string {
	if s, ok := body["display_name"].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}

func stringList(v any) []string {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		if s := scalarString(e); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func isYAML(name string) bool {
	ext := filepath.Ext(name)
	return ext == ".yml" || ext == ".yaml"
}
