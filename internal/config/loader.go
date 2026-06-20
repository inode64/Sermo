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

var defaultServiceDirs = []string{"services"}
var defaultAppDirs = []string{"apps"}
var defaultMountDirs = []string{"mounts"}

// Option customizes Load.
type Option func(*loadOptions)

type loadOptions struct {
	catalogDirs []string
}

// WithCatalogDirs overrides the catalog search directories (the definition
// directories holding services/apps/libs/patterns) declared in the global
// config's paths.catalog. Relative entries are resolved against the current
// working directory (not the config file), since the override is a caller/CLI
// choice. It backs `sermod --catalog` and lets tests load the installed config
// (which points at /usr/share/sermo/catalog) while keeping definitions in the
// source tree.
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

	catalogDirs := global.Catalog
	if len(catalogDirs) == 0 {
		catalogDirs = []string{"/usr/share/sermo/catalog", "/etc/sermo/catalog-available"}
	}
	serviceDirs := global.Services
	if len(serviceDirs) == 0 {
		serviceDirs = defaultConfigDirs(globalPath, defaultServiceDirs)
		global.Services = append([]string(nil), serviceDirs...)
	}
	appDirs := global.Apps
	if len(appDirs) == 0 {
		appDirs = defaultConfigDirs(globalPath, defaultAppDirs)
		global.Apps = append([]string(nil), appDirs...)
	}
	notifierDirs := global.Notifiers
	watchDirs := appendPathLists(global.Storages, global.Networks, global.Watches)
	mountDirs := global.Mounts
	if len(mountDirs) == 0 {
		mountDirs = defaultConfigDirs(globalPath, defaultMountDirs)
		global.Mounts = append([]string(nil), mountDirs...)
	}

	cfg := &Config{
		Global:    global,
		Daemons:   map[string]*Document{},
		Apps:      map[string]*Document{},
		Libraries: map[string]*Document{},
		Patterns:  map[string]*Document{},
		Services:  map[string]*Document{},
		Mounts:    map[string]*Document{},
	}

	for _, dir := range catalogDirs {
		if err := cfg.loadDir(dir); err != nil {
			return nil, err
		}
	}
	loadedServiceDirs := uniquePathList(serviceDirs)
	for _, dir := range loadedServiceDirs {
		if err := cfg.loadServiceDir(dir); err != nil {
			return nil, err
		}
	}
	for _, dir := range uniquePathList(appDirs) {
		if err := cfg.loadAppDir(dir); err != nil {
			return nil, err
		}
	}
	for _, dir := range uniquePathList(notifierDirs) {
		if err := cfg.loadGlobalFragmentDir(dir, "notifiers"); err != nil {
			return nil, err
		}
	}
	for _, dir := range uniquePathList(watchDirs) {
		if err := cfg.loadGlobalFragmentDir(dir, "watches"); err != nil {
			return nil, err
		}
	}
	for _, dir := range mountDirs {
		if err := cfg.loadMountDir(dir); err != nil {
			return nil, err
		}
	}
	cfg.applyOSSelectors()
	cfg.bakeBuiltins()
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
		g.Services = cfgval.StringList(paths["services"])
		g.Apps = cfgval.StringList(paths["apps"])
		g.Notifiers = cfgval.StringList(paths["notifiers"])
		g.Storages = cfgval.StringList(paths["storages"])
		g.Networks = cfgval.StringList(paths["networks"])
		g.Watches = cfgval.StringList(paths["watches"])
		g.Mounts = cfgval.StringList(paths["mounts"])
		g.Runtime = cfgval.String(paths["runtime"])
		g.State = cfgval.String(paths["state"])
		g.Templates = cfgval.String(paths["templates"])
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

// resolveConfigPaths makes catalog/services/apps/runtime/state/templates paths
// absolute. Relative entries are resolved against the global config file's
// directory so a tree like examples/sermo.yml with `services: [services]` loads
// examples/services when run from the repository.
func resolveConfigPaths(globalPath string, g *Global) {
	base := filepath.Dir(filepath.Clean(globalPath))
	g.Catalog = resolvePathList(base, g.Catalog)
	g.Services = resolvePathList(base, g.Services)
	g.Apps = resolvePathList(base, g.Apps)
	g.Notifiers = resolvePathList(base, g.Notifiers)
	g.Storages = resolvePathList(base, g.Storages)
	g.Networks = resolvePathList(base, g.Networks)
	g.Watches = resolvePathList(base, g.Watches)
	g.Mounts = resolvePathList(base, g.Mounts)
	if g.Runtime != "" {
		g.Runtime = resolveConfigPath(base, g.Runtime)
	}
	if g.State != "" {
		g.State = resolveConfigPath(base, g.State)
	}
	if g.Templates != "" {
		g.Templates = resolveConfigPath(base, g.Templates)
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

func defaultConfigDirs(globalPath string, dirs []string) []string {
	return resolvePathList(filepath.Dir(filepath.Clean(globalPath)), dirs)
}

func appendPathLists(lists ...[]string) []string {
	var out []string
	for _, list := range lists {
		out = append(out, list...)
	}
	return out
}

func uniquePathList(list []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(list))
	for _, dir := range list {
		if dir == "" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		out = append(out, dir)
	}
	return out
}

// loadDir reads every *.yml/*.yaml document in dir, recursing into
// subdirectories. A `services`/`apps`/`libs`/`patterns` subdirectory tags the
// catalog documents it holds with that category; files directly in dir default
// to CategoryService. A missing directory is not an error (a host may not have
// user catalog documents), but an unreadable one is.
func (c *Config) loadDir(dir string) error {
	return c.loadCategoryDir(dir, "")
}

func (c *Config) loadServiceDir(dir string) error {
	return c.loadServiceDirEntries(dir)
}

func (c *Config) loadAppDir(dir string) error {
	return c.loadAppDirEntries(dir)
}

func (c *Config) loadGlobalFragmentDir(dir string, section string) error {
	return c.loadGlobalFragmentDirEntries(dir, section)
}

func (c *Config) loadMountDir(dir string) error {
	return c.loadMountDirEntries(dir)
}

func (c *Config) loadServiceDirEntries(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read service config dir %s: %w", dir, err)
	}

	var names []string
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
		if doc.Kind != kindService {
			return fmt.Errorf("%s: service config directories only support kind: service", doc.Path)
		}
		c.add(doc)
	}
	for _, name := range subdirs {
		if err := c.loadServiceDirEntries(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) loadAppDirEntries(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read app config dir %s: %w", dir, err)
	}

	var names []string
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
		if doc.Kind != kindApp {
			return fmt.Errorf("%s: app config directories only support kind: app", doc.Path)
		}
		c.add(doc)
	}
	for _, name := range subdirs {
		if err := c.loadAppDirEntries(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) loadGlobalFragmentDirEntries(dir string, section string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s config dir %s: %w", section, dir, err)
	}

	var names []string
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
		handled, err := c.mergeGlobalFragmentSection(doc, section)
		if err != nil {
			return err
		}
		if !handled {
			return fmt.Errorf("%s: %s config directories only support top-level %s", doc.Path, section, section)
		}
	}
	for _, name := range subdirs {
		if err := c.loadGlobalFragmentDirEntries(filepath.Join(dir, name), section); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) loadMountDirEntries(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read mount config dir %s: %w", dir, err)
	}

	var names []string
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
		if doc.Kind != kindMount {
			return fmt.Errorf("%s: mount config directories only support kind: mount", doc.Path)
		}
		c.add(doc)
	}
	for _, name := range subdirs {
		if err := c.loadMountDirEntries(filepath.Join(dir, name)); err != nil {
			return err
		}
	}
	return nil
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
		// Catalog definitions take their kind from the subdirectory
		// (daemon/app/lib/patterns), so each lives in its own registry.
		doc.Kind = kindForCategory(doc.Category)
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

func (c *Config) mergeGlobalFragmentSection(doc *Document, section string) (bool, error) {
	if doc.Kind != "" {
		return false, nil
	}
	if _, present := doc.Body[section]; !present {
		return false, nil
	}
	for key := range doc.Body {
		if key != section {
			return true, fmt.Errorf("%s: %s fragments only support top-level %s, got %q", doc.Path, section, section, key)
		}
	}
	return c.mergeGlobalMap(doc, section)
}

func (c *Config) mergeGlobalMap(doc *Document, section string) (bool, error) {
	raw := expandEnvTree(doc.Body[section])
	entries, ok := raw.(map[string]any)
	if !ok {
		return true, fmt.Errorf("%s: %s must be a mapping", doc.Path, section)
	}
	if len(entries) != 1 {
		return true, fmt.Errorf("%s: %s fragments must contain exactly one entry", doc.Path, section)
	}
	dst, _ := c.Global.Raw[section].(map[string]any)
	if dst == nil {
		dst = map[string]any{}
	}
	label := includedGlobalSectionLabel(section)
	for name, entry := range entries {
		if _, exists := dst[name]; exists {
			return true, fmt.Errorf("%s: %s %q is already defined", doc.Path, label, name)
		}
		dst[name] = entry
	}
	c.Global.Raw[section] = dst
	return true, nil
}

func includedGlobalSectionLabel(section string) string {
	switch section {
	case "watches":
		return "watch"
	case "notifiers":
		return "notifier"
	default:
		return strings.TrimSuffix(section, "s")
	}
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
	switch doc.Kind {
	case kindDaemon:
		indexDocument(c.Daemons, &c.DaemonNames, doc)
		addCatalogAliases(c.Daemons, doc)
	case kindApp:
		indexDocument(c.Apps, &c.AppNames, doc)
		addCatalogAliases(c.Apps, doc)
	case kindLibrary:
		indexDocument(c.Libraries, &c.LibraryNames, doc)
	case kindPatterns:
		indexDocument(c.Patterns, &c.PatternNames, doc)
	case kindService:
		indexDocument(c.Services, &c.ServiceNames, doc)
	case kindMount:
		indexDocument(c.Mounts, &c.MountNames, doc)
	}
	c.docs = append(c.docs, doc)
}

func indexDocument(reg map[string]*Document, names *[]string, doc *Document) {
	*names = append(*names, doc.Name)
	if doc.Name == "" {
		return
	}
	existing, exists := reg[doc.Name]
	// A canonical document may replace a registry entry that was created by a
	// previous document's catalog_aliases, but duplicate canonical names still keep
	// the first document and are reported by validation.
	if !exists || existing.Name != doc.Name {
		reg[doc.Name] = doc
	}
}

func addCatalogAliases(reg map[string]*Document, doc *Document) {
	for _, alias := range cfgval.StringList(doc.Body["catalog_aliases"]) {
		if alias == "" || alias == doc.Name {
			continue
		}
		if _, exists := reg[alias]; !exists {
			reg[alias] = doc
		}
	}
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

// CategoryLabel returns the optional UI grouping category from a document body,
// falling back to fallback when the field is absent or blank.
func CategoryLabel(body map[string]any, fallback string) string {
	if s, ok := body["category"].(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return fallback
}

func isYAML(name string) bool {
	ext := filepath.Ext(name)
	return ext == ".yml" || ext == ".yaml"
}
