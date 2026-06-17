// Package config loads, merges, resolves and validates Sermo's YAML
// configuration into flat per-service definitions.
//
// The pipeline is deliberately generic: documents are decoded into
// map[string]any trees, merged recursively (section 9), resolved through the
// defaults -> uses/clone -> overrides precedence (section 8), and finally have
// their ${var} references expanded once (section 10). Typed extraction happens
// only where validation needs it. This keeps merge semantics uniform across
// every section instead of per-field.
package config

import (
	"maps"
	"sermo/internal/cfgval"
	"slices"
)

// docKind identifies the document kinds. Catalog definitions carry a kind per
// subdirectory — `daemon` (services), `app` (tools/runtimes), `lib` (shared
// libraries) — so they live in separate registries and a service may share a
// name with the app that owns its binary (e.g. `apache` daemon + `apache` app).
// `service` is an enabled instance (usually under an include dir such as
// services/) that `uses` a daemon.
const (
	kindDaemon   = "daemon"
	kindApp      = "app"
	kindLibrary  = "lib"
	kindService  = "service"
	kindPatterns = "patterns"
	kindMount    = "mount"
)

// Catalog categories mirror the catalog subdirectory a definition is loaded
// from (catalog/services, catalog/apps, catalog/libs, catalog/patterns); files
// directly under a catalog root default to CategoryService. The category tracks
// the kind for display and category-scoped listings.
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
		return kindDaemon
	}
}

// categoryFromDir maps a catalog subdirectory name to a category, or "" when the
// directory is not a recognized category (its files inherit the default).
func categoryFromDir(name string) string {
	switch name {
	case "services":
		return CategoryService
	case "apps":
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
	"kind":            {},
	"name":            {},
	"uses":            {},
	"clone":           {},
	"catalog_aliases": {},
}

// perServiceDefaults are the only parts of global `defaults` that merge into a
// service (section 8). Engine-wide settings never reach individual services.
var perServiceDefaults = []string{"stop_policy", "policy", "rule_window", "remediation"}

// Document is a single loaded daemon or service in raw, unexpanded form.
type Document struct {
	Kind     string
	Name     string
	Path     string
	Category string // service | app | library (daemons only; from the directory)
	Body     map[string]any
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
	if v, ok := tree["monitor"].(string); ok && v != "" {
		return v
	}
	return MonitorEnabled
}

// Global is the effective global configuration (sermo.yml plus conf.d), kept
// mostly generic so its `defaults` block merges into services unchanged.
type Global struct {
	Path      string
	Raw       map[string]any
	Defaults  map[string]any
	Catalog   []string
	Includes  []string
	Mounts    []string
	Runtime   string
	State     string
	Templates string
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
// `service`, the first candidate of a per-init `service` map, or the legacy
// `service.name`; falling back to the given name.
func ServiceUnit(tree map[string]any, fallback string) string {
	switch s := tree["service"].(type) {
	case string:
		if s != "" {
			return s
		}
	case map[string]any:
		if name, _ := s["name"].(string); name != "" { // legacy form
			return name
		}
		for _, backend := range []string{"systemd", "openrc"} {
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
//   - `service: name` (scalar) or legacy `service: { name: ... }` →
//     a single trusted candidate (units the probe cannot surface, e.g.
//     sysv-generated, are not rejected). trust = true.
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
		if _, ok := s["systemd"]; ok {
			return cfgval.StringList(s[backend]), false
		}
		if _, ok := s["openrc"]; ok {
			return cfgval.StringList(s[backend]), false
		}
		if name, _ := s["name"].(string); name != "" { // legacy form
			return []string{name}, true
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
// found by scanning the processes section for pidfile selectors), the files/globs
// that must be absent (`files_absent`), the master cleanup switch
// (`clean_after_stop`), and the files/directories to delete on stop
// (`clean_on_stop`, each a path string or a `{path, recursive}` mapping). The
// `clean` bool is the single opt-in that enables all active deletion after a
// clean stop — both removing stale `pidfile_absent`/`files_absent` leftovers and
// deleting the `clean_on_stop` list; with it off the invariants are verified and
// warned about but nothing is deleted. All zero when absent.
func StopInvariants(tree map[string]any) (pidfilePaths, files []string, clean bool, cleanPaths []CleanPath) {
	sp, ok := tree["stop_policy"].(map[string]any)
	if !ok {
		return nil, nil, false, nil
	}
	clean, _ = sp["clean_after_stop"].(bool)
	files = cfgval.StringList(sp["files_absent"])
	if pa, _ := sp["pidfile_absent"].(bool); pa {
		if procs, ok := tree["processes"].(map[string]any); ok {
			for _, v := range procs {
				if m, ok := v.(map[string]any); ok && cfgval.AsString(m["type"]) == "pidfile" {
					pidfilePaths = append(pidfilePaths, cfgval.StringList(m["path"])...)
				}
			}
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

// CascadeTargets returns the additional Sermo services declared in `also_apply`,
// which receive the same cascading action (start/stop/restart) as this service
// via their own guarded operation. Reload is deliberately not cascaded. Empty
// when absent.
func CascadeTargets(tree map[string]any) []string {
	return cfgval.StringList(tree["also_apply"])
}

// Notifiers returns the global `notifiers` section (nil when absent) — the
// single way to reach it; validation, the web backend, the wizard and
// notify.Build all consume this shape.
func (c *Config) Notifiers() map[string]any {
	if c == nil {
		return nil
	}
	m, _ := c.Global.Raw["notifiers"].(map[string]any)
	return m
}

// SortedServiceNames returns the configured service names alphabetically —
// the stable iteration order the daemon, web backend and diagnostics share
// (ServiceNames keeps load order for reporting).
func (c *Config) SortedServiceNames() []string {
	if c == nil {
		return nil
	}
	return slices.Sorted(maps.Keys(c.Services))
}

// Config is the full loaded configuration set.
type Config struct {
	Global    Global
	Daemons   map[string]*Document // kind daemon (service definitions)
	Apps      map[string]*Document // kind app (tools/runtimes: binary + version)
	Libraries map[string]*Document // kind lib (shared libraries)
	Patterns  map[string]*Document // kind patterns (output-analysis rule sets)
	Services  map[string]*Document // kind service (enabled instances)
	Mounts    map[string]*Document // kind mount (fstab-backed mount units)
	// Load order per registry, for stable reporting.
	DaemonNames  []string
	AppNames     []string
	LibraryNames []string
	PatternNames []string
	ServiceNames []string
	MountNames   []string
	docs         []*Document // every document in load order
}
