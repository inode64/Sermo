package config

import (
	"context"
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

var defaultCatalogDir = "/usr/share/sermo/catalog"

const (
	yamlFileExt     = ".yml"
	yamlLongFileExt = ".yaml"
)

const (
	pathsLabelApps      = SectionPaths + "." + pathKeyApps
	pathsLabelNotifiers = SectionPaths + "." + pathKeyNotifiers
	pathsLabelServices  = SectionPaths + "." + pathKeyServices
	pathsLabelWatches   = SectionPaths + "." + pathKeyWatches
)

var defaultServiceDirs = []string{pathKeyServices}
var defaultAppDirs = []string{pathKeyApps}

// Option customizes Load.
type Option func(*loadOptions)

type loadOptions struct {
	catalogDirs  []string
	pathDirs     map[string][]string
	serviceUnits map[string][]string
	loadCtx      context.Context
}

// WithCatalogDirs overrides the compiled catalog search directory for tests and
// staged packaging checks. It is intentionally not exposed in YAML or daemon
// flags: production loads catalog definitions from defaultCatalogDir.
func WithCatalogDirs(dirs ...string) Option {
	return func(o *loadOptions) { o.catalogDirs = dirs }
}

func withPathDirs(kind string) Option {
	return func(o *loadOptions) {
		if o.pathDirs == nil {
			o.pathDirs = map[string][]string{}
		}
		o.pathDirs[kind] = nil
	}
}

// WithLoadContext binds service-unit discovery during lazy catalog resolution to
// the caller's context. Production callers pass the daemon lifetime context.
func WithLoadContext(ctx context.Context) Option {
	return func(o *loadOptions) { o.loadCtx = ctx }
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
	applyPathDirOverride(&global, o.pathDirs)

	catalogPaths := pathSpecsFromPaths([]string{defaultCatalogDir})
	if len(o.catalogDirs) > 0 {
		catalogPaths = pathSpecsFromPaths(absOverrideDirs(o.catalogDirs))
	}
	_, servicePathsOverridden := o.pathDirs[pathKeyServices]
	servicePaths := global.ServicePaths
	if len(servicePaths) == 0 && !servicePathsOverridden {
		servicePaths = pathSpecsFromPaths(defaultConfigDirs(globalPath, defaultServiceDirs))
		global.Services = pathsFromSpecs(servicePaths)
		global.ServicePaths = append([]PathSpec(nil), servicePaths...)
	}
	_, appPathsOverridden := o.pathDirs[pathKeyApps]
	appPaths := global.AppPaths
	if len(appPaths) == 0 && !appPathsOverridden {
		appPaths = pathSpecsFromPaths(defaultConfigDirs(globalPath, defaultAppDirs))
		global.Apps = pathsFromSpecs(appPaths)
		global.AppPaths = append([]PathSpec(nil), appPaths...)
	}
	notifierPaths := global.NotifierPaths
	watchPaths := global.WatchPaths

	loadCtx := o.loadCtx
	if loadCtx == nil {
		loadCtx = context.Background()
	}
	cfg := &Config{
		Global:          global,
		CatalogServices: map[string]*Document{},
		Apps:            map[string]*Document{},
		Libraries:       map[string]*Document{},
		Patterns:        map[string]*Document{},
		Services:        map[string]*Document{},
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
		if err := cfg.loadNotifierDir(spec.Path, spec.Recursive); err != nil {
			return nil, err
		}
	}
	for _, spec := range uniquePathSpecs(watchPaths) {
		if err := cfg.loadWatchDir(spec.Path, spec.Recursive); err != nil {
			return nil, err
		}
	}
	if err := cfg.applyOSSelectors(); err != nil {
		return nil, err
	}
	cfg.bakeBuiltins()
	cfg.expandBindir()
	cfg.materializeVersionTemplates(loadCtx)
	return cfg, nil
}

func loadGlobal(path string) (Global, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return Global{}, fmt.Errorf("read global config %s: %w", path, err)
	}
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Global{}, parseGlobalConfigError(path, err)
	}
	if raw == nil {
		raw = map[string]any{}
	}
	// Resolve ${env:...} across the global config so secrets (notifier DSNs/
	// webhooks, web passwords, …) come from the environment, never the file.
	expandEnvTree(raw)

	g := Global{Path: path, Raw: raw}
	if defaults, ok := raw[sectionDefaults].(map[string]any); ok {
		g.Defaults = defaults
	} else {
		g.Defaults = map[string]any{}
	}
	if paths, ok := raw[sectionPaths].(map[string]any); ok {
		if g.ServicePaths, err = pathSpecList(paths[pathKeyServices], pathsLabelServices); err != nil {
			return Global{}, parseGlobalConfigError(path, err)
		}
		if g.AppPaths, err = pathSpecList(paths[pathKeyApps], pathsLabelApps); err != nil {
			return Global{}, parseGlobalConfigError(path, err)
		}
		if g.NotifierPaths, err = pathSpecList(paths[pathKeyNotifiers], pathsLabelNotifiers); err != nil {
			return Global{}, parseGlobalConfigError(path, err)
		}
		if g.WatchPaths, err = pathSpecList(paths[pathKeyWatches], pathsLabelWatches); err != nil {
			return Global{}, parseGlobalConfigError(path, err)
		}
		g.Services = pathsFromSpecs(g.ServicePaths)
		g.Apps = pathsFromSpecs(g.AppPaths)
		g.Notifiers = pathsFromSpecs(g.NotifierPaths)
		g.Watches = pathsFromSpecs(g.WatchPaths)
		g.Runtime = cfgval.String(paths[pathKeyRuntime])
		g.State = cfgval.String(paths[pathKeyState])
		g.Templates = cfgval.String(paths[pathKeyTemplates])
	}
	resolveConfigPaths(path, &g)
	return g, nil
}

func parseGlobalConfigError(path string, err error) error {
	return fmt.Errorf("parse global config %s: %w", path, err)
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
	apply(pathKeyServices, &g.Services, &g.ServicePaths)
	apply(pathKeyApps, &g.Apps, &g.AppPaths)
	apply(pathKeyNotifiers, &g.Notifiers, &g.NotifierPaths)
	apply(pathKeyWatches, &g.Watches, &g.WatchPaths)
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

// resolveConfigPaths makes services/apps/runtime/state/templates paths absolute.
// Relative entries are resolved against the global config file's directory so a
// tree like examples/sermo.yml with `services: [services]` loads
// examples/services when run from the repository.
func resolveConfigPaths(globalPath string, g *Global) {
	base := configBaseDir(globalPath)
	g.Services = resolvePathList(base, g.Services)
	g.Apps = resolvePathList(base, g.Apps)
	g.Notifiers = resolvePathList(base, g.Notifiers)
	g.Watches = resolvePathList(base, g.Watches)
	g.ServicePaths = resolvePathSpecs(base, g.ServicePaths)
	g.AppPaths = resolvePathSpecs(base, g.AppPaths)
	g.NotifierPaths = resolvePathSpecs(base, g.NotifierPaths)
	g.WatchPaths = resolvePathSpecs(base, g.WatchPaths)
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
		case keyPath, keyRecursive:
		default:
			return PathSpec{}, fmt.Errorf("%s.%s is not supported; use path and recursive", field, key)
		}
	}
	path, ok := m[keyPath].(string)
	if !ok || path == "" {
		return PathSpec{}, fmt.Errorf("%s.path must be a non-empty string", field)
	}
	var recursive bool
	if raw, present := m[keyRecursive]; present {
		recursive, ok = raw.(bool)
		if !ok {
			return PathSpec{}, fmt.Errorf(validationBooleanFormat, field+"."+keyRecursive)
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
// directories. A missing directory is not an error (tests and partial installs
// may intentionally omit catalog content), but an unreadable one is.
func (c *Config) loadDir(dir string, recursive bool) error {
	return c.loadCategoryDir(dir, "", recursive)
}

func (c *Config) loadServiceDir(dir string, recursive bool) error {
	return c.loadKindDirEntries(dir, kindService, kindService, recursive)
}

func (c *Config) loadAppDir(dir string, recursive bool) error {
	return c.loadKindDirEntries(dir, kindApp, kindApp, recursive)
}

func (c *Config) loadNotifierDir(dir string, recursive bool) error {
	return loadDocumentTree(dir, pathKeyNotifiers, recursive, func(doc *Document) error {
		handled, err := c.mergeNotifierFragment(doc)
		if err != nil {
			return err
		}
		if !handled {
			return fmt.Errorf("%s: %s config directories only support top-level %s", doc.Path, pathKeyNotifiers, pathKeyNotifiers)
		}
		return nil
	})
}

func (c *Config) loadWatchDir(dir string, recursive bool) error {
	return loadDocumentTree(dir, pathKeyWatches, recursive, c.mergeWatchDocument)
}

func (c *Config) loadKindDirEntries(dir, label, kind string, recursive bool) error {
	return loadDocumentTree(dir, label, recursive, func(doc *Document) error {
		if err := assignKind(doc, kind); err != nil {
			return err
		}
		c.add(doc)
		return nil
	})
}

// loadDocumentTree reads YAML documents from dir, optionally descending into
// its subdirectories. consume owns the kind-specific merge or registration.
func loadDocumentTree(dir, label string, recursive bool, consume func(*Document) error) error {
	names, subdirs, err := configDirEntries(dir, label)
	if err != nil {
		return err
	}
	for _, name := range names {
		doc, err := loadDocument(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		if err := consume(doc); err != nil {
			return err
		}
	}
	if !recursive {
		return nil
	}
	for _, name := range subdirs {
		if err := loadDocumentTree(filepath.Join(dir, name), label, recursive, consume); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) loadCategoryDir(dir, category string, recursive bool) error {
	names, subdirs, err := configDirEntries(dir, "")
	if err != nil {
		return err
	}

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

func configDirEntries(dir, label string) (names, subdirs []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, nil
		}
		if label == "" {
			return nil, nil, fmt.Errorf("read config dir %s: %w", dir, err)
		}
		return nil, nil, fmt.Errorf("read %s config dir %s: %w", label, dir, err)
	}

	names = make([]string, 0, len(entries))
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
	return names, subdirs, nil
}

func (c *Config) mergeWatchDocument(doc *Document) error {
	if _, present := doc.Body[pathKeyWatches]; present {
		return fmt.Errorf("%s: watch documents use top-level name/check fields, not a watches map", doc.Path)
	}
	if declared := cfgval.String(doc.Body[keyKind]); declared != "" && declared != kindWatch {
		return fmt.Errorf("%s: located under a watches directory but declares kind: %s", doc.Path, declared)
	}
	if doc.Name == "" {
		return fmt.Errorf("%s: watch documents must define name", doc.Path)
	}
	if !validDocumentName(doc.Name) {
		return fmt.Errorf("%s: watch name %q must be a simple name without path separators", doc.Path, doc.Name)
	}
	entry := cloneMap(doc.Body)
	delete(entry, keyKind)
	delete(entry, keyName)
	expandEnvTree(entry)

	dst, _ := c.Global.Raw[pathKeyWatches].(map[string]any)
	if dst == nil {
		dst = map[string]any{}
	}
	if _, exists := dst[doc.Name]; exists {
		return fmt.Errorf("%s: watch %q is already defined", doc.Path, doc.Name)
	}
	dst[doc.Name] = entry
	c.Global.Raw[pathKeyWatches] = dst
	return nil
}

func (c *Config) mergeNotifierFragment(doc *Document) (bool, error) {
	if _, present := doc.Body[pathKeyNotifiers]; !present {
		return false, nil
	}
	for key := range doc.Body {
		if key != pathKeyNotifiers {
			return true, fmt.Errorf("%s: %s fragments only support top-level %s, got %q", doc.Path, pathKeyNotifiers, pathKeyNotifiers, key)
		}
	}
	return c.mergeNotifierMap(doc)
}

func (c *Config) mergeNotifierMap(doc *Document) (bool, error) {
	raw := expandEnvTree(doc.Body[pathKeyNotifiers])
	entries, ok := raw.(map[string]any)
	if !ok {
		return true, fmt.Errorf("%s: %s must be a mapping", doc.Path, pathKeyNotifiers)
	}
	if len(entries) != 1 {
		return true, fmt.Errorf("%s: %s fragments must contain exactly one entry", doc.Path, pathKeyNotifiers)
	}
	dst, _ := c.Global.Raw[pathKeyNotifiers].(map[string]any)
	if dst == nil {
		dst = map[string]any{}
	}
	for name, entry := range entries {
		if _, exists := dst[name]; exists {
			return true, fmt.Errorf("%s: notifier %q is already defined", doc.Path, name)
		}
		dst[name] = entry
	}
	c.Global.Raw[pathKeyNotifiers] = dst
	return true, nil
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
		Name: cfgval.String(body[keyName]),
		Path: path,
		Body: body,
	}, nil
}

// assignKind sets a document's kind from the location it was read from. The
// `kind:` key is redundant — the directory already determines the kind — so it
// may be omitted. When still present it must match, which catches a file dropped
// into the wrong directory (e.g. a mount under services/).
func assignKind(doc *Document, expected string) error {
	if declared := cfgval.String(doc.Body[keyKind]); declared != "" && declared != expected {
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
	if s, ok := body[keyDisplayName].(string); ok && strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}

// CategoryLabel returns the optional UI grouping category from a document body,
// falling back to fallback when the field is absent or blank.
func CategoryLabel(body map[string]any, fallback string) string {
	if s, ok := body[keyCategory].(string); ok && strings.TrimSpace(s) != "" {
		return strings.TrimSpace(s)
	}
	return fallback
}

func isYAML(name string) bool {
	ext := filepath.Ext(name)
	return ext == yamlFileExt || ext == yamlLongFileExt
}
