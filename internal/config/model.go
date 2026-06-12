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

import "sermo/internal/cfgval"

// docKind identifies the document kinds. Catalog definitions carry a kind per
// subdirectory — `daemon` (services), `app` (tools/runtimes), `lib` (shared
// libraries) — so they live in separate registries and a service may share a
// name with the app that owns its binary (e.g. `apache` daemon + `apache` app).
// `service` is an enabled instance (apps-enabled) that `uses` a daemon.
const (
	kindDaemon   = "daemon"
	kindApp      = "app"
	kindLibrary  = "lib"
	kindService  = "service"
	kindPatterns = "patterns"
)

// Daemon categories mirror the catalog subdirectory a definition is loaded from
// (catalog/services, catalog/apps, catalog/libs); files directly under a catalog
// root default to CategoryService. The category tracks the kind for display and
// category-scoped listings.
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
	"kind":  {},
	"name":  {},
	"uses":  {},
	"clone": {},
}

// perServiceDefaults are the only parts of global `defaults` that merge into a
// service (section 8). Engine-wide settings never reach individual services.
var perServiceDefaults = []string{"stop_policy", "policy", "rule_window"}

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
	Path     string
	Raw      map[string]any
	Defaults map[string]any
	Catalog  []string
	Includes []string
	Runtime  string
	State    string
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

// CascadeTargets returns the additional Sermo services declared in `also_apply`,
// which receive the same action (start/stop/restart) as this service via their
// own guarded operation. Empty when absent.
func CascadeTargets(tree map[string]any) []string {
	return cfgval.StringList(tree["also_apply"])
}

// Config is the full loaded configuration set.
type Config struct {
	Global    Global
	Daemons   map[string]*Document // kind daemon (service definitions)
	Apps      map[string]*Document // kind app (tools/runtimes: binary + version)
	Libraries map[string]*Document // kind lib (shared libraries)
	Patterns  map[string]*Document // kind patterns (output-analysis rule sets)
	Services  map[string]*Document // kind service (enabled instances)
	// Load order per registry, for stable reporting.
	DaemonNames  []string
	AppNames     []string
	LibraryNames []string
	PatternNames []string
	ServiceNames []string
	docs         []*Document // every document in load order
}
