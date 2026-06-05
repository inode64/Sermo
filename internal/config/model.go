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
	Kind string
	Name string
	Path string
	Body map[string]any
}

// DefaultRuntime is the runtime root used when paths.runtime is unset.
const DefaultRuntime = "/run/sermo"

// Global is the effective global configuration (sermo.yml plus conf.d), kept
// mostly generic so its `defaults` block merges into services unchanged.
type Global struct {
	Path     string
	Raw      map[string]any
	Defaults map[string]any
	Profiles []string
	Enabled  []string
	Runtime  string
}

// RuntimeDir returns the runtime root, falling back to the default when unset.
func (g Global) RuntimeDir() string {
	if g.Runtime == "" {
		return DefaultRuntime
	}
	return g.Runtime
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
