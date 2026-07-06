// Package config loads, merges, resolves and validates Sermo's YAML
// configuration into flat per-service definitions.
//
// The pipeline is deliberately generic: documents are decoded into
// map[string]any trees, merged recursively, resolved through the
// defaults -> uses/clone -> overrides precedence, and finally have
// their ${var} references expanded once. Typed extraction happens
// only where validation needs it. This keeps merge semantics uniform across
// every section instead of per-field.
package config

import (
	"maps"
	"sermo/internal/cfgval"
	"slices"
)

// docKind identifies the document kinds. A `service` document is either a
// reusable catalog definition (catalog/services, distinguished by a non-empty
// Category) or a configured instance under paths.services that `uses` a catalog
// service. `app` (tools/runtimes) and `lib` (shared libraries) are the other
// catalog kinds, so a service may share a name with the app that owns its binary
// (e.g. `apache` catalog service + `apache` app).
const (
	kindApp      = "app"
	kindLibrary  = "lib"
	kindService  = "service"
	kindPatterns = "patterns"
	kindStorage  = "storage"
)

// sectionStopPolicy is the per-service/mount block declaring the stopped-state
// invariants (pidfile/file cleanup) the engine enforces.
const sectionStopPolicy = "stop_policy"

// sectionPolicy is the remediation policy block; sectionRuleWindow is the
// firing-window fallback block.
const (
	sectionPolicy     = "policy"
	sectionRuleWindow = "rule_window"
)

// stop_policy timeout and kill-guard field keys.
const (
	keyGracefulTimeout = "graceful_timeout"
	keyTermTimeout     = "term_timeout"
	keyKillTimeout     = "kill_timeout"
	keyForceKill       = "force_kill"
	keyKillOnlyIf      = "kill_only_if"
)

// keyDryRun is the per-target flag that simulates automatic actions.
const keyDryRun = "dry_run"

// Check-gate / check-entry field keys.
const (
	keyRequires = "requires"
	keyOptional = "optional"
	keyVerify   = "verify"
)

// Storage document block keys.
const (
	keyCapacity = "capacity"
	keyUsage    = "usage"
)

// Per-target monitoring metadata keys.
const (
	keyMonitor  = "monitor"
	keyInterval = "interval"
	keyEnabled  = "enabled"
)

// storage mount / umount block field keys.
const (
	keyMount        = "mount"
	keyRefcount     = "refcount"
	keyUmount       = "umount"
	keyAllowSIGKILL = "allow_sigkill"
	keyAllowLazy    = "allow_lazy"
)

// sectionMetrics is the multi-metric watch block: a map of metric name to its
// per-metric condition/action (used by net/swap/icmp watches).
const sectionMetrics = "metrics"

// sectionChecks is the service/watch health-check block: a map of check name to
// its check definition. Distinct from the paths.* keys (no paths.checks exists).
const sectionChecks = "checks"

// sectionPreflight is the service block of gating checks that must pass before a
// start/restart/reload operation runs.
const sectionPreflight = "preflight"

// sectionProcesses is the service block of named process selectors (exe/user)
// used for discovery and kill matching.
const sectionProcesses = "processes"

// sectionVariables is the ${var} definition block (on defaults, a service, a
// storage doc or an app) expanded during resolution.
const sectionVariables = "variables"

// Catalog categories mirror the catalog subdirectory a definition is loaded
// from (catalog/services, catalog/apps, catalog/libs, catalog/patterns). The
// category tracks the kind for display and category-scoped listings.
const (
	CategoryService  = "service"
	CategoryApp      = "app"
	CategoryLibrary  = "library"
	CategoryPatterns = "patterns"
)

// kindForCategory maps a catalog category to the document kind it is registered
// under, so the subdirectory alone determines a definition's kind.
func kindForCategory(category string) string {
	switch category {
	case CategoryApp:
		return kindApp
	case CategoryLibrary:
		return kindLibrary
	case CategoryPatterns:
		return kindPatterns
	default:
		return kindService
	}
}

// categoryFromDir maps a catalog subdirectory name to a category, or "" when the
// directory is not a recognized category (its files inherit the default).
func categoryFromDir(name string) string {
	switch name {
	case pathKeyServices:
		return CategoryService
	case pathKeyApps:
		return CategoryApp
	case "libs":
		return CategoryLibrary
	case "patterns":
		return CategoryPatterns
	default:
		return ""
	}
}

// metaKeys are the document keys that control resolution and are not part of a
// service's merged body.
var metaKeys = map[string]struct{}{
	"aliases": {},
	"name":    {},
	"uses":    {},
	"clone":   {},
}

// perServiceDefaults are the only parts of global `defaults` that merge into a
// service. Engine-wide settings never reach individual services.
var perServiceDefaults = []string{keyDryRun, sectionStopPolicy, sectionPolicy, sectionRuleWindow}

// perStorageDefaults are the only parts of global `defaults` that merge into a
// storage target.
var perStorageDefaults = []string{keyDryRun}

// Document is a single loaded catalog definition or configured target in raw,
// unexpanded form.
type Document struct {
	Kind                 string
	Name                 string
	Path                 string
	Category             string // service | app | library | patterns (catalog only; from the directory)
	Body                 map[string]any
	TemplateBaseName     string
	TemplateCurrentLabel bool
}

// registryKey is the namespace a document is indexed and de-duplicated under.
// Catalog services and configured services share kind `service` but must stay
// separate (a catalog template and the instance that `uses` it can share a
// name), so a catalog service keys on "catalog/service"; every other document —
// including catalog vs deployed apps, which do share one registry — keys on its
// kind.
func (d *Document) registryKey() string {
	if d.Category == CategoryService {
		return catalogServiceKey
	}
	return d.Kind
}

// catalogServiceKey is the registry namespace for catalog service definitions.
const catalogServiceKey = "catalog/service"

// DocumentAliases returns the alternate public names declared by a catalog or
// configured document. Aliases identify the document during resolution, but do
// not merge into the runtime service body.
func DocumentAliases(doc *Document) []string {
	if doc == nil {
		return nil
	}
	return cfgval.StringList(doc.Body["aliases"])
}

// CanonicalCatalogName returns the canonical name for a catalog document in
// category, accepting exact names and `aliases`.
func (c *Config) CanonicalCatalogName(category, name string) (string, bool) {
	if c == nil || name == "" {
		return "", false
	}
	if doc := c.catalogRegistry(category)[name]; doc != nil {
		return documentCanonicalName(doc, name), true
	}
	return canonicalAlias(c.catalogRegistry(category), c.catalogNames(category), name)
}

// CanonicalServiceName returns the configured service name for name, accepting
// exact service names, configured service aliases, and a conservative catalog
// alias fallback. The fallback only maps a catalog service alias to a configured
// service when that service uses the catalog service and has the same canonical
// name as it, avoiding surprising alias matches for instance names such as
// `apache-main`.
func (c *Config) CanonicalServiceName(name string) (string, bool) {
	if c == nil || name == "" {
		return "", false
	}
	if doc := c.Services[name]; doc != nil {
		return documentCanonicalName(doc, name), true
	}
	if canonical, ok := canonicalAlias(c.Services, c.ServiceNames, name); ok {
		return canonical, true
	}
	return c.canonicalServiceNameFromCatalogAlias(name)
}

func (c *Config) canonicalServiceNameFromCatalogAlias(alias string) (string, bool) {
	catalogName, ok := c.CanonicalCatalogName(CategoryService, alias)
	if !ok {
		return "", false
	}
	var match string
	seen := map[string]bool{}
	for _, serviceName := range c.ServiceNames {
		if seen[serviceName] {
			continue
		}
		seen[serviceName] = true
		doc := c.Services[serviceName]
		docName := documentCanonicalName(doc, serviceName)
		if doc == nil || docName != catalogName {
			continue
		}
		uses := cfgval.String(doc.Body["uses"])
		if uses == "" {
			continue
		}
		canonicalUses, ok := c.CanonicalCatalogName(CategoryService, uses)
		if !ok || canonicalUses != catalogName {
			continue
		}
		if match != "" && match != docName {
			return "", false
		}
		match = docName
	}
	return match, match != ""
}

func canonicalAlias(reg map[string]*Document, names []string, alias string) (string, bool) {
	var match string
	seen := map[*Document]bool{}
	for _, name := range names {
		doc := reg[name]
		if doc == nil || seen[doc] {
			continue
		}
		seen[doc] = true
		docName := documentCanonicalName(doc, name)
		for _, candidate := range DocumentAliases(doc) {
			if candidate != alias {
				continue
			}
			if match != "" && match != docName {
				return "", false
			}
			match = docName
		}
	}
	return match, match != ""
}

func documentCanonicalName(doc *Document, fallback string) string {
	if doc != nil && doc.Name != "" {
		return doc.Name
	}
	return fallback
}

func (c *Config) catalogNames(category string) []string {
	switch category {
	case CategoryApp:
		return c.AppNames
	case CategoryLibrary:
		return c.LibraryNames
	case CategoryPatterns:
		return c.PatternNames
	default:
		return c.CatalogServiceNames
	}
}

// DefaultRuntime is the runtime root used when paths.runtime is unset.
const DefaultRuntime = "/run/sermo"

// DefaultState is the persistent state root used when paths.state is unset. It
// lives under /var/lib so it survives reboots, unlike the runtime root on tmpfs.
const DefaultState = "/var/lib/sermo"

// DefaultTemplates is the directory holding notification templates.
const DefaultTemplates = "/etc/sermo/templates"

// Monitor modes for a service/watch `monitor` flag. They set the daemon's
// startup behavior:
//   - MonitorEnabled : always monitor on startup (the default)
//   - MonitorDisabled: never monitor
//   - MonitorPrevious: restore the persisted runtime state from the last run
const (
	MonitorEnabled  = "enabled"
	MonitorDisabled = "disabled"
	MonitorPrevious = "previous"
)

// MonitorMode returns a resolved entry's `monitor` flag, defaulting to
// MonitorEnabled so services/watches are monitored unless told otherwise.
func MonitorMode(tree map[string]any) string {
	if v, ok := tree[keyMonitor].(string); ok && v != "" {
		return v
	}
	return MonitorEnabled
}

// DryRun reports whether automatic actions for this configured target are simulated.
func DryRun(tree map[string]any) bool {
	return cfgval.Bool(tree[keyDryRun])
}

// Global is the effective global configuration (sermo.yml plus conf.d), kept
// mostly generic so its `defaults` block merges into services unchanged.
type Global struct {
	Path          string
	Raw           map[string]any
	Defaults      map[string]any
	Catalog       []string
	Services      []string
	Apps          []string
	Notifiers     []string
	Storages      []string
	Networks      []string
	Watches       []string
	CatalogPaths  []PathSpec
	ServicePaths  []PathSpec
	AppPaths      []PathSpec
	NotifierPaths []PathSpec
	StoragePaths  []PathSpec
	NetworkPaths  []PathSpec
	WatchPaths    []PathSpec
	Runtime       string
	State         string
	Templates     string
}

// PathSpec is one configured directory under paths.*. Recursive defaults to
// false; operators opt in per directory when they want nested *.yml files.
type PathSpec struct {
	Path      string
	Recursive bool
}

// RuntimeDir returns the runtime root, falling back to the default when unset.
func (g Global) RuntimeDir() string {
	if g.Runtime == "" {
		return DefaultRuntime
	}
	return g.Runtime
}

// StateDir returns the persistent state root, falling back to the default when
// unset.
func (g Global) StateDir() string {
	if g.State == "" {
		return DefaultState
	}
	return g.State
}

// TemplateDir returns the notification template directory, falling back to the
// installed default when unset.
func (g Global) TemplateDir() string {
	if g.Templates == "" {
		return DefaultTemplates
	}
	return g.Templates
}

// ServiceUnit returns a service's primary (display/seed) unit name: the scalar
// `service`, or the first candidate of a per-init `service` map; falling back to
// the given name.
func ServiceUnit(tree map[string]any, fallback string) string {
	switch s := tree["service"].(type) {
	case string:
		if s != "" {
			return s
		}
	case map[string]any:
		for _, backend := range []string{backendSystemd, backendOpenRC} {
			if list := cfgval.StringList(s[backend]); len(list) > 0 {
				return list[0]
			}
		}
	}
	return fallback
}

// ServiceCandidates returns the unit-name candidates to try for backend, and
// whether to trust the first candidate when none can be probed.
//
//   - `service: name` (scalar) → a single trusted candidate (units the probe
//     cannot surface, e.g. sysv-generated, are not rejected). trust = true.
//   - `service: { systemd: [...], openrc: [...] }` (per-init) → the list for
//     backend, requiring a match (trust = false). A backend with no entry is
//     not available: the candidate list is empty.
func ServiceCandidates(tree map[string]any, backend, fallback string) (candidates []string, trust bool) {
	switch s := tree["service"].(type) {
	case string:
		if s != "" {
			return []string{s}, true
		}
	case map[string]any:
		if _, ok := s[backendSystemd]; ok {
			return cfgval.StringList(s[backend]), false
		}
		if _, ok := s[backendOpenRC]; ok {
			return cfgval.StringList(s[backend]), false
		}
	}
	return []string{fallback}, true
}

// AdditionalUnits returns the auxiliary init units declared in `also_service`
// for the active backend (e.g. `also_service: { systemd: [docker.socket] }`).
// These are plain init units the operation acts on alongside the primary unit
// (wrap order: up before the primary, down after) — distinct from `also_apply`,
// which cascades to other Sermo services. Empty when absent or when the backend
// has no list.
func AdditionalUnits(tree map[string]any, backend string) []string {
	m, ok := tree["also_service"].(map[string]any)
	if !ok {
		return nil
	}
	return cfgval.StringList(m[backend])
}

// CleanPath is one `clean_on_stop` entry: a path (or glob, when not recursive)
// deleted after a clean stop; Recursive deletes a directory tree.
type CleanPath struct {
	Path      string
	Recursive bool
}

// StopInvariants reads the stopped-state invariants from `stop_policy`: the
// pidfile path(s) that must be absent after stop (when `pidfile_absent: true`,
// found from the service's top-level `pidfile:`), the files/globs
// that must be absent (`files_absent`), the master cleanup switch
// (`clean_after_stop`), and the files/directories to delete on stop
// (`clean_on_stop`, each a path string or a `{path, recursive}` mapping). The
// `clean` bool is the single opt-in that enables all active deletion after a
// clean stop — both removing stale `pidfile_absent`/`files_absent` leftovers and
// deleting the `clean_on_stop` list; with it off the invariants are verified and
// warned about but nothing is deleted. All zero when absent.
func StopInvariants(tree map[string]any) (pidfilePaths, files []string, clean bool, cleanPaths []CleanPath) {
	sp, ok := tree[sectionStopPolicy].(map[string]any)
	if !ok {
		return nil, nil, false, nil
	}
	clean, _ = sp["clean_after_stop"].(bool)
	files = cfgval.StringList(sp["files_absent"])
	if pa, _ := sp["pidfile_absent"].(bool); pa {
		pidfilePaths = append(pidfilePaths, cfgval.StringList(tree["pidfile"])...)
		for _, role := range sortedPidfileRoles(tree) {
			pidfilePaths = append(pidfilePaths, cfgval.StringList(tree["pidfiles"].(map[string]any)[role])...)
		}
	}
	if raw, ok := sp["clean_on_stop"].([]any); ok {
		for _, item := range raw {
			switch e := item.(type) {
			case string:
				if e != "" {
					cleanPaths = append(cleanPaths, CleanPath{Path: e})
				}
			case map[string]any:
				p := cfgval.AsString(e["path"])
				rec, _ := e["recursive"].(bool)
				if p != "" {
					cleanPaths = append(cleanPaths, CleanPath{Path: p, Recursive: rec})
				}
			}
		}
	}
	return pidfilePaths, files, clean, cleanPaths
}

func sortedPidfileRoles(tree map[string]any) []string {
	pidfiles, ok := tree["pidfiles"].(map[string]any)
	if !ok {
		return nil
	}
	return slices.Sorted(maps.Keys(pidfiles))
}

// CascadeTargets returns the additional Sermo services declared in `also_apply`,
// which receive the same cascading action (start/stop/restart) as this service
// via their own guarded operation. Reload is deliberately not cascaded. Empty
// when absent.
func CascadeTargets(tree map[string]any) []string {
	return cfgval.StringList(tree["also_apply"])
}

// Notifiers returns the global `notifiers` section plus built-in notifiers. It is
// the single way to reach notifier definitions; validation, the web backend, the
// wizard and notify.Build all consume this shape.
func (c *Config) Notifiers() map[string]any {
	if c == nil {
		return nil
	}
	out := map[string]any{
		notifierTypeTTY:  map[string]any{"type": notifierTypeTTY},
		notifierTypeWall: map[string]any{"type": notifierTypeWall},
	}
	m, _ := c.Global.Raw[pathKeyNotifiers].(map[string]any)
	for name, entry := range m {
		out[name] = entry
	}
	return out
}

// SortedServiceNames returns the configured service names alphabetically —
// the stable iteration order the daemon and web backend share
// (ServiceNames keeps load order for reporting).
func (c *Config) SortedServiceNames() []string {
	if c == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(c.Services))
}

// Config is the full loaded configuration set.
type Config struct {
	Global          Global
	CatalogServices map[string]*Document // catalog service definitions (catalog/services)
	Apps            map[string]*Document // kind app (tools/runtimes: binary + version)
	Libraries       map[string]*Document // kind lib (shared libraries)
	Patterns        map[string]*Document // kind patterns (output-analysis rule sets)
	Services        map[string]*Document // kind service (enabled instances)
	Storages        map[string]*Document // kind storage (capacity and optional fstab-backed mount unit)
	docs            []*Document          // every document in load order

	materializedNameCollisions []materializedNameCollision
	validationIssues           []Issue
	serviceUnits               map[string][]string

	// Load order per registry, for stable reporting.
	CatalogServiceNames []string
	AppNames            []string
	LibraryNames        []string
	PatternNames        []string
	ServiceNames        []string
	StorageNames        []string
}

type materializedNameCollision struct {
	Kind         string
	Name         string
	TemplateName string
	TemplatePath string
	ExistingPath string
}
