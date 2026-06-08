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

// docKind identifies the two document kinds.
const (
	kindProfile = "profile"
	kindService = "service"
)

// Profile categories, derived from the subdirectory a profile is loaded from
// (profiles/services, profiles/apps, profiles/libs). Files directly under a
// profiles root default to CategoryService.
const (
	CategoryService = "service"
	CategoryApp     = "app"
	CategoryLibrary = "library"
)

// categoryFromDir maps a profiles subdirectory name to a category, or "" when the
// directory is not a recognized category (its files inherit the default).
func categoryFromDir(name string) string {
	switch name {
	case "services":
		return CategoryService
	case "apps":
		return CategoryApp
	case "libs":
		return CategoryLibrary
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

// Document is a single loaded profile or service in raw, unexpanded form.
type Document struct {
	Kind     string
	Name     string
	Path     string
	Category string // service | app | library (profiles only; from the directory)
	Body     map[string]any
}

// DefaultRuntime is the runtime root used when paths.runtime is unset.
const DefaultRuntime = "/run/sermo"

// DefaultState is the persistent state root used when paths.state is unset. It
// lives under /var/lib so it survives reboots, unlike the runtime root on tmpfs.
const DefaultState = "/var/lib/sermo"

// Monitor modes for a service's per-service `monitor` flag. They set the
// daemon's startup behavior:
//   - MonitorEnabled : always monitor on startup (the default)
//   - MonitorDisabled: never monitor
//   - MonitorPrevious: restore the persisted runtime state from the last run
const (
	MonitorEnabled  = "enabled"
	MonitorDisabled = "disabled"
	MonitorPrevious = "previous"
)

// MonitorMode returns a resolved service's `monitor` flag, defaulting to
// MonitorEnabled so services are monitored unless told otherwise.
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
	Profiles []string
	Enabled  []string
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
			if list := stringList(s[backend]); len(list) > 0 {
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
			return stringList(s[backend]), false
		}
		if _, ok := s["openrc"]; ok {
			return stringList(s[backend]), false
		}
		if name, _ := s["name"].(string); name != "" { // legacy form
			return []string{name}, true
		}
	}
	return []string{fallback}, true
}

// Config is the full loaded configuration set.
type Config struct {
	Global       Global
	Profiles     map[string]*Document
	Services     map[string]*Document
	ProfileNames []string // load order, for stable reporting
	ServiceNames []string
	docs         []*Document // every document in load order
}
