package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sermo/internal/cfgval"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// DefaultGlobalPath is the standard location of the global configuration.
const DefaultGlobalPath = "/etc/sermo/sermo.yml"

// Option customizes Load.
type Option func(*loadOptions)

type loadOptions struct {
	catalogDirs []string
}

// WithCatalogDirs overrides the catalog search directories (the definition
// directories holding services/apps/libs) declared in the global config's
// paths.catalog. Relative entries are resolved against the current working
// directory (not the config file), since the override is a caller/CLI choice. It
// backs `sermod --catalog` and lets tests load the installed config (which points
// at /usr/share/sermo/catalog) while keeping definitions in the source tree.
func WithCatalogDirs(dirs ...string) Option {
	return func(o *loadOptions) { o.catalogDirs = dirs }
}

// Load reads the global configuration at globalPath and every daemon and
// service document reachable from its `paths`. Parsing/IO failures abort; the
// returned Config carries documents in raw, unexpanded form for resolution.
func Load(globalPath string, opts ...Option) (*Config, error) {
	var o loadOptions
	for _, opt := range opts {
		opt(&o)
	}

	global, err := loadGlobal(globalPath)
	if err != nil {
		return nil, err
	}
	if len(o.catalogDirs) > 0 {
		global.Catalog = absCatalogDirs(o.catalogDirs)
	}

	cfg := &Config{
		Global:    global,
		Daemons:   map[string]*Document{},
		Apps:      map[string]*Document{},
		Libraries: map[string]*Document{},
		Patterns:  map[string]*Document{},
		Services:  map[string]*Document{},
	}

	catalogDirs := global.Catalog
	if len(catalogDirs) == 0 {
		catalogDirs = []string{"/usr/share/sermo/catalog", "/etc/sermo/catalog-available"}
	}
	includeDirs := global.Includes
	if len(includeDirs) == 0 {
		includeDirs = []string{"/etc/sermo/apps-enabled"}
	}

	for _, dir := range catalogDirs {
		if err := cfg.loadDir(dir); err != nil {
			return nil, err
		}
	}
	for _, dir := range includeDirs {
		if err := cfg.loadIncludeDir(dir); err != nil {
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
		g.Catalog = cfgval.StringList(paths["catalog"])
		g.Includes = cfgval.StringList(paths["includes"])
		if len(g.Includes) == 0 {
			g.Includes = cfgval.StringList(paths["enabled"])
		}
		g.Runtime = cfgval.String(paths["runtime"])
		g.State = cfgval.String(paths["state"])
	}
	resolveConfigPaths(path, &g)
	return g, nil
}

// absCatalogDirs cleans an override list, making relative entries absolute
// against the current working directory and dropping empty ones.
func absCatalogDirs(dirs []string) []string {
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if d == "" {
			continue
		}
		if abs, err := filepath.Abs(d); err == nil {
			out = append(out, abs)
			continue
		}
		out = append(out, filepath.Clean(d))
	}
	return out
}

// resolveConfigPaths makes catalog/includes/runtime/state paths absolute. Relative
// entries are resolved against the global config file's directory so a tree like
// configs/sermo.yml with `includes: [apps-enabled]` loads configs/apps-enabled
// when run from the repository.
func resolveConfigPaths(globalPath string, g *Global) {
	base := filepath.Dir(filepath.Clean(globalPath))
	g.Catalog = resolvePathList(base, g.Catalog)
	g.Includes = resolvePathList(base, g.Includes)
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
// subdirectories. A `services`/`apps`/`libs` subdirectory tags the daemons it
// holds with that category; files directly in dir default to CategoryService. A
// missing directory is not an error (a host may not have user daemons), but an
// unreadable one is.
func (c *Config) loadDir(dir string) error {
	return c.loadCategoryDir(dir, "", false)
}

func (c *Config) loadIncludeDir(dir string) error {
	return c.loadCategoryDir(dir, "", true)
}

func (c *Config) loadCategoryDir(dir, category string, allowGlobalFragments bool) error {
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
		if allowGlobalFragments {
			handled, err := c.mergeIncludedGlobalFragment(doc)
			if err != nil {
				return err
			}
			if handled {
				continue
			}
		}
		doc.Category = effectiveCategory(category)
		// Catalog definitions take their kind from the subdirectory (daemon/app/
		// lib), so each lives in its own registry; included documents keep their
		// declared kind (service instances).
		if !allowGlobalFragments {
			doc.Kind = kindForCategory(doc.Category)
		}
		c.add(doc)
	}
	for _, name := range subdirs {
		sub := category
		if sub == "" {
			sub = categoryFromDir(name) // only the top level names a category
		}
		if err := c.loadCategoryDir(filepath.Join(dir, name), sub, allowGlobalFragments); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) mergeIncludedGlobalFragment(doc *Document) (bool, error) {
	if doc.Kind != "" {
		return false, nil
	}
	raw, present := doc.Body["watches"]
	if !present {
		return false, nil
	}
	for key := range doc.Body {
		if key != "watches" {
			return true, fmt.Errorf("%s: enabled watch fragments only support top-level watches, got %q", doc.Path, key)
		}
	}
	watches, ok := raw.(map[string]any)
	if !ok {
		return true, fmt.Errorf("%s: watches must be a mapping", doc.Path)
	}
	dst, _ := c.Global.Raw["watches"].(map[string]any)
	if dst == nil {
		dst = map[string]any{}
	}
	for name, entry := range watches {
		if _, exists := dst[name]; exists {
			return true, fmt.Errorf("%s: watch %q is already defined", doc.Path, name)
		}
		dst[name] = entry
	}
	c.Global.Raw["watches"] = dst
	return true, nil
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
	kind := cfgval.String(body["kind"])
	return &Document{
		Kind: kind,
		Name: cfgval.String(body["name"]),
		Path: path,
		Body: body,
	}, nil
}

// add indexes a document by name. The first document under each name wins for
// indexing; duplicate-name detection is reported by validation, which sees the
// later document's path.
func (c *Config) add(doc *Document) {
	index := func(m map[string]*Document, names *[]string) {
		if _, exists := m[doc.Name]; !exists && doc.Name != "" {
			m[doc.Name] = doc
		}
		*names = append(*names, doc.Name)
	}
	switch doc.Kind {
	case kindDaemon:
		index(c.Daemons, &c.DaemonNames)
	case kindApp:
		index(c.Apps, &c.AppNames)
	case kindLibrary:
		index(c.Libraries, &c.LibraryNames)
	case kindPatterns:
		index(c.Patterns, &c.PatternNames)
	case kindService:
		index(c.Services, &c.ServiceNames)
	}
	c.docs = append(c.docs, doc)
}

// DaemonsInCategory returns the names of catalog definitions in a category
// (service | app | library), sorted, for category-scoped listings such as
// `apps` and `libs`.
func (c *Config) DaemonsInCategory(category string) []string {
	var names []string
	switch category {
	case CategoryApp:
		names = append(names, c.AppNames...)
	case CategoryLibrary:
		names = append(names, c.LibraryNames...)
	case CategoryPatterns:
		names = append(names, c.PatternNames...)
	default:
		names = append(names, c.DaemonNames...)
	}
	sort.Strings(names)
	return uniqueStrings(names)
}

// uniqueStrings returns the sorted input with adjacent duplicates removed.
func uniqueStrings(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || sorted[i-1] != s {
			out = append(out, s)
		}
	}
	return out
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

func isYAML(name string) bool {
	ext := filepath.Ext(name)
	return ext == ".yml" || ext == ".yaml"
}
