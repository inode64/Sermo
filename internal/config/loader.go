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
	catalogDirs  []string
	pathDirs     map[string][]string
	serviceUnits map[string][]string
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

func withPathDirs(kind string, dirs ...string) Option {
	return func(o *loadOptions) {
		if o.pathDirs == nil {
			o.pathDirs = map[string][]string{}
		}
		o.pathDirs[kind] = dirs
	}
}

// WithServiceUnits provides active backend units for service-derived catalog service
// template materialization. It is mainly used by tests; production loads query
// the active init backend lazily.
func WithServiceUnits(backend string, units []string) Option {
	return func(o *loadOptions) {
		if o.serviceUnits == nil {
			o.serviceUnits = map[string][]string{}
		}
		o.serviceUnits[backend] = normalizeServiceUnits(units)
	}
}

// Load reads the global configuration at globalPath and every catalog service and
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
		global.Catalog = absOverrideDirs(o.catalogDirs)
		global.CatalogPaths = pathSpecsFromPaths(global.Catalog)
	}
	applyPathDirOverride(&global, o.pathDirs)

	catalogPaths := global.CatalogPaths
	if len(catalogPaths) == 0 {
		catalogPaths = pathSpecsFromPaths([]string{"/usr/share/sermo/catalog", "/etc/sermo/catalog-available"})
	}
	_, servicePathsOverridden := o.pathDirs["services"]
	servicePaths := global.ServicePaths
	if len(servicePaths) == 0 && !servicePathsOverridden {
		servicePaths = pathSpecsFromPaths(defaultConfigDirs(globalPath, defaultServiceDirs))
		global.Services = pathsFromSpecs(servicePaths)
		global.ServicePaths = append([]PathSpec(nil), servicePaths...)
	}
	_, appPathsOverridden := o.pathDirs["apps"]
	appPaths := global.AppPaths
	if len(appPaths) == 0 && !appPathsOverridden {
		appPaths = pathSpecsFromPaths(defaultConfigDirs(globalPath, defaultAppDirs))
		global.Apps = pathsFromSpecs(appPaths)
		global.AppPaths = append([]PathSpec(nil), appPaths...)
	}
	notifierPaths := global.NotifierPaths
	watchPaths := appendPathSpecLists(global.StoragePaths, global.NetworkPaths, global.WatchPaths)
	_, mountPathsOverridden := o.pathDirs["mounts"]
	mountPaths := global.MountPaths
	if len(mountPaths) == 0 && !mountPathsOverridden {
		mountPaths = pathSpecsFromPaths(defaultConfigDirs(globalPath, defaultMountDirs))
		global.Mounts = pathsFromSpecs(mountPaths)
		global.MountPaths = append([]PathSpec(nil), mountPaths...)
	}

	cfg := &Config{
		Global:          global,
		CatalogServices: map[string]*Document{},
		Apps:            map[string]*Document{},
		Libraries:       map[string]*Document{},
		Patterns:        map[string]*Document{},
		Services:        map[string]*Document{},
		Mounts:          map[string]*Document{},
		serviceUnits:    cloneServiceUnits(o.serviceUnits),
	}

	for _, spec := range uniquePathSpecs(catalogPaths) {
		if err := cfg.loadDir(spec.Path, spec.Recursive); err != nil {
			return nil, err
		}
	}
	for _, spec := range uniquePathSpecs(servicePaths) {
		if err := cfg.loadServiceDir(spec.Path, spec.Recursive); err != nil {
			return nil, err
		}
	}
	for _, spec := range uniquePathSpecs(appPaths) {
		if err := cfg.loadAppDir(spec.Path, spec.Recursive); err != nil {
			return nil, err
		}
	}
	for _, spec := range uniquePathSpecs(notifierPaths) {
		if err := cfg.loadGlobalFragmentDir(spec.Path, "notifiers", spec.Recursive); err != nil {
			return nil, err
		}
	}
	for _, spec := range uniquePathSpecs(watchPaths) {
		if err := cfg.loadGlobalFragmentDir(spec.Path, "watches", spec.Recursive); err != nil {
			return nil, err
		}
	}
	for _, spec := range uniquePathSpecs(mountPaths) {
		if err := cfg.loadMountDir(spec.Path, spec.Recursive); err != nil {
			return nil, err
		}
	}
	cfg.applyOSSelectors()
	cfg.bakeBuiltins()
	cfg.expandBindir()
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
		if g.CatalogPaths, err = pathSpecList(paths["catalog"], "paths.catalog"); err != nil {
			return Global{}, fmt.Errorf("parse global config %s: %w", path, err)
		}
		if g.ServicePaths, err = pathSpecList(paths["services"], "paths.services"); err != nil {
			return Global{}, fmt.Errorf("parse global config %s: %w", path, err)
		}
		if g.AppPaths, err = pathSpecList(paths["apps"], "paths.apps"); err != nil {
			return Global{}, fmt.Errorf("parse global config %s: %w", path, err)
		}
		if g.NotifierPaths, err = pathSpecList(paths["notifiers"], "paths.notifiers"); err != nil {
			return Global{}, fmt.Errorf("parse global config %s: %w", path, err)
		}
		if g.StoragePaths, err = pathSpecList(paths["storages"], "paths.storages"); err != nil {
			return Global{}, fmt.Errorf("parse global config %s: %w", path, err)
		}
		if g.NetworkPaths, err = pathSpecList(paths["networks"], "paths.networks"); err != nil {
			return Global{}, fmt.Errorf("parse global config %s: %w", path, err)
		}
		if g.WatchPaths, err = pathSpecList(paths["watches"], "paths.watches"); err != nil {
			return Global{}, fmt.Errorf("parse global config %s: %w", path, err)
		}
		if g.MountPaths, err = pathSpecList(paths["mounts"], "paths.mounts"); err != nil {
			return Global{}, fmt.Errorf("parse global config %s: %w", path, err)
		}
		g.Catalog = pathsFromSpecs(g.CatalogPaths)
		g.Services = pathsFromSpecs(g.ServicePaths)
		g.Apps = pathsFromSpecs(g.AppPaths)
		g.Notifiers = pathsFromSpecs(g.NotifierPaths)
		g.Storages = pathsFromSpecs(g.StoragePaths)
		g.Networks = pathsFromSpecs(g.NetworkPaths)
		g.Watches = pathsFromSpecs(g.WatchPaths)
		g.Mounts = pathsFromSpecs(g.MountPaths)
		g.Runtime = cfgval.String(paths["runtime"])
		g.State = cfgval.String(paths["state"])
		g.Templates = cfgval.String(paths["templates"])
	}
	resolveConfigPaths(path, &g)
	return g, nil
}

func applyPathDirOverride(g *Global, overrides map[string][]string) {
	if len(overrides) == 0 {
		return
	}
	apply := func(kind string, paths *[]string, specs *[]PathSpec) {
		dirs, ok := overrides[kind]
		if !ok {
			return
		}
		*paths = absOverrideDirs(dirs)
		*specs = pathSpecsFromPaths(*paths)
	}
	apply("services", &g.Services, &g.ServicePaths)
	apply("apps", &g.Apps, &g.AppPaths)
	apply("notifiers", &g.Notifiers, &g.NotifierPaths)
	apply("storages", &g.Storages, &g.StoragePaths)
	apply("networks", &g.Networks, &g.NetworkPaths)
	apply("watches", &g.Watches, &g.WatchPaths)
	apply("mounts", &g.Mounts, &g.MountPaths)
}

// absOverrideDirs cleans an override list, making relative entries absolute
// against the current working directory and dropping empty ones.
func absOverrideDirs(dirs []string) []string {
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
	base := configBaseDir(globalPath)
	g.Catalog = resolvePathList(base, g.Catalog)
	g.Services = resolvePathList(base, g.Services)
	g.Apps = resolvePathList(base, g.Apps)
	g.Notifiers = resolvePathList(base, g.Notifiers)
	g.Storages = resolvePathList(base, g.Storages)
	g.Networks = resolvePathList(base, g.Networks)
	g.Watches = resolvePathList(base, g.Watches)
	g.Mounts = resolvePathList(base, g.Mounts)
	g.CatalogPaths = resolvePathSpecs(base, g.CatalogPaths)
	g.ServicePaths = resolvePathSpecs(base, g.ServicePaths)
	g.AppPaths = resolvePathSpecs(base, g.AppPaths)
	g.NotifierPaths = resolvePathSpecs(base, g.NotifierPaths)
	g.StoragePaths = resolvePathSpecs(base, g.StoragePaths)
	g.NetworkPaths = resolvePathSpecs(base, g.NetworkPaths)
	g.WatchPaths = resolvePathSpecs(base, g.WatchPaths)
	g.MountPaths = resolvePathSpecs(base, g.MountPaths)
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
	return resolvePathList(configBaseDir(globalPath), dirs)
}

func configBaseDir(globalPath string) string {
	base := filepath.Dir(filepath.Clean(globalPath))
	if abs, err := filepath.Abs(base); err == nil {
		return abs
	}
	return base
}

func pathSpecList(v any, field string) ([]PathSpec, error) {
	switch t := v.(type) {
	case nil:
		return nil, nil
	case string:
		if t == "" {
			return nil, nil
		}
		return []PathSpec{{Path: t}}, nil
	case map[string]any:
		spec, err := pathSpecFromMap(t, field)
		if err != nil {
			return nil, err
		}
		return []PathSpec{spec}, nil
	case []any:
		out := make([]PathSpec, 0, len(t))
		for i, item := range t {
			spec, ok, err := pathSpecFromListItem(item, fmt.Sprintf("%s[%d]", field, i))
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, spec)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%s must be a path string, {path, recursive} mapping, or list of those", field)
	}
}

func pathSpecFromListItem(v any, field string) (PathSpec, bool, error) {
	switch t := v.(type) {
	case string:
		if t == "" {
			return PathSpec{}, false, nil
		}
		return PathSpec{Path: t}, true, nil
	case map[string]any:
		spec, err := pathSpecFromMap(t, field)
		return spec, err == nil, err
	default:
		return PathSpec{}, false, fmt.Errorf("%s must be a path string or {path, recursive} mapping", field)
	}
}

func pathSpecFromMap(m map[string]any, field string) (PathSpec, error) {
	for key := range m {
		switch key {
		case "path", "recursive":
		default:
			return PathSpec{}, fmt.Errorf("%s.%s is not supported; use path and recursive", field, key)
		}
	}
	path, ok := m["path"].(string)
	if !ok || path == "" {
		return PathSpec{}, fmt.Errorf("%s.path must be a non-empty string", field)
	}
	var recursive bool
	if raw, present := m["recursive"]; present {
		recursive, ok = raw.(bool)
		if !ok {
			return PathSpec{}, fmt.Errorf("%s.recursive must be a boolean", field)
		}
	}
	return PathSpec{Path: path, Recursive: recursive}, nil
}

func pathSpecsFromPaths(paths []string) []PathSpec {
	out := make([]PathSpec, 0, len(paths))
	for _, path := range paths {
		if path != "" {
			out = append(out, PathSpec{Path: path})
		}
	}
	return out
}

func pathsFromSpecs(specs []PathSpec) []string {
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		if spec.Path != "" {
			out = append(out, spec.Path)
		}
	}
	return out
}

func resolvePathSpecs(base string, specs []PathSpec) []PathSpec {
	if len(specs) == 0 {
		return specs
	}
	out := make([]PathSpec, len(specs))
	for i, spec := range specs {
		out[i] = spec
		out[i].Path = resolveConfigPath(base, spec.Path)
	}
	return out
}

func appendPathSpecLists(lists ...[]PathSpec) []PathSpec {
	var out []PathSpec
	for _, list := range lists {
		out = append(out, list...)
	}
	return out
}

func uniquePathSpecs(specs []PathSpec) []PathSpec {
	seen := map[string]int{}
	out := make([]PathSpec, 0, len(specs))
	for _, spec := range specs {
		if spec.Path == "" {
			continue
		}
		if idx, ok := seen[spec.Path]; ok {
			out[idx].Recursive = out[idx].Recursive || spec.Recursive
			continue
		}
		seen[spec.Path] = len(out)
		out = append(out, spec)
	}
	return out
}

// loadDir reads catalog documents from the explicit services/apps/libs/patterns
// category directories. Recursive controls descent below those base catalog
// directories. A missing directory is not an error (a host may not have user
// catalog documents), but an unreadable one is.
func (c *Config) loadDir(dir string, recursive bool) error {
	return c.loadCategoryDir(dir, "", recursive)
}

func (c *Config) loadServiceDir(dir string, recursive bool) error {
	return c.loadServiceDirEntries(dir, recursive)
}

func (c *Config) loadAppDir(dir string, recursive bool) error {
	return c.loadAppDirEntries(dir, recursive)
}

func (c *Config) loadGlobalFragmentDir(dir string, section string, recursive bool) error {
	return c.loadGlobalFragmentDirEntries(dir, section, recursive)
}

func (c *Config) loadMountDir(dir string, recursive bool) error {
	return c.loadMountDirEntries(dir, recursive)
}

func (c *Config) loadServiceDirEntries(dir string, recursive bool) error {
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
		if err := assignKind(doc, kindService); err != nil {
			return err
		}
		c.add(doc)
	}
	if !recursive {
		return nil
	}
	for _, name := range subdirs {
		if err := c.loadServiceDirEntries(filepath.Join(dir, name), recursive); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) loadAppDirEntries(dir string, recursive bool) error {
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
		if err := assignKind(doc, kindApp); err != nil {
			return err
		}
		c.add(doc)
	}
	if !recursive {
		return nil
	}
	for _, name := range subdirs {
		if err := c.loadAppDirEntries(filepath.Join(dir, name), recursive); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) loadGlobalFragmentDirEntries(dir string, section string, recursive bool) error {
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
	if !recursive {
		return nil
	}
	for _, name := range subdirs {
		if err := c.loadGlobalFragmentDirEntries(filepath.Join(dir, name), section, recursive); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) loadMountDirEntries(dir string, recursive bool) error {
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
		if err := assignKind(doc, kindMount); err != nil {
			return err
		}
		c.add(doc)
	}
	if !recursive {
		return nil
	}
	for _, name := range subdirs {
		if err := c.loadMountDirEntries(filepath.Join(dir, name), recursive); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) loadCategoryDir(dir, category string, recursive bool) error {
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

	if category == "" && len(names) > 0 {
		return fmt.Errorf("%s: catalog documents must live under services, apps, libs, or patterns", filepath.Join(dir, names[0]))
	}
	for _, name := range names {
		doc, err := loadDocument(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		doc.Category = category
		// Catalog definitions take their kind from the subdirectory
		// (service/app/lib/patterns), so each lives in its own registry; any
		// `kind:` in the file is redundant and ignored.
		doc.Kind = kindForCategory(doc.Category)
		c.add(doc)
	}
	for _, name := range subdirs {
		sub := category
		if sub == "" {
			sub = categoryFromDir(name)
			if sub == "" {
				continue
			}
		} else if !recursive {
			continue
		}
		if err := c.loadCategoryDir(filepath.Join(dir, name), sub, recursive); err != nil {
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
	// Kind is derived from the document's location by the caller (assignKind);
	// any `kind:` still present in the body is honored there only as an optional
	// consistency check, so loadDocument leaves Document.Kind unset.
	return &Document{
		Name: cfgval.String(body["name"]),
		Path: path,
		Body: body,
	}, nil
}

// assignKind sets a document's kind from the location it was read from. The
// `kind:` key is redundant — the directory already determines the kind — so it
// may be omitted. When still present it must match, which catches a file dropped
// into the wrong directory (e.g. a mount under services/).
func assignKind(doc *Document, expected string) error {
	if declared := cfgval.String(doc.Body["kind"]); declared != "" && declared != expected {
		return fmt.Errorf("%s: located under a %s directory but declares kind: %s", doc.Path, expected, declared)
	}
	doc.Kind = expected
	return nil
}

// add indexes a document by name. The first document under each name wins for
// indexing; duplicate-name detection is reported by validation, which sees the
// later document's path.
func (c *Config) add(doc *Document) {
	// Route by registry namespace, not kind: catalog services and configured
	// services share kind `service` but live in separate registries, keyed by
	// catalog category vs configured location.
	switch doc.registryKey() {
	case catalogServiceKey:
		indexDocument(c.CatalogServices, &c.CatalogServiceNames, doc)
	case kindApp:
		indexDocument(c.Apps, &c.AppNames, doc)
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
	if _, exists := reg[doc.Name]; !exists {
		reg[doc.Name] = doc
	}
}

// CatalogNamesInCategory returns the names of catalog definitions in a category
// (service | app | library), sorted, for category-scoped listings such as
// `apps` and `libs`.
func (c *Config) CatalogNamesInCategory(category string) []string {
	var names []string
	switch category {
	case CategoryApp:
		names = append(names, c.AppNames...)
	case CategoryLibrary:
		names = append(names, c.LibraryNames...)
	case CategoryPatterns:
		names = append(names, c.PatternNames...)
	default:
		names = append(names, c.CatalogServiceNames...)
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
