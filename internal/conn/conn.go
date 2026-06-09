// Package conn provides connection-protocol probes (MySQL/MariaDB, and future
// protocols) behind a small registry. Each protocol implements Protocol and
// registers itself; the checks package looks a protocol up by name to build a
// generic connection check. It is a leaf package: it depends on neither checks
// nor config, so both can import it without a cycle.
package conn

import (
	"context"
	"sort"
	"sync"
)

// Config is the connection target for a protocol probe. Fields that do not apply
// to a protocol are ignored by it.
type Config struct {
	Host     string
	Port     int
	Socket   string // Unix socket path; when set, protocols dial it instead of host:port
	User     string
	Password string
	Database string
	Query    string // protocol-specific lookup target (e.g. the DNS name to resolve)
	TLS      string // "" / "false" (plaintext), "true", "skip-verify"
	// Interface, when set, is the network interface the probe must egress through
	// (Linux SO_BINDTODEVICE) — for multi-homed hosts. Empty means default routing.
	Interface string
	Params    map[string]string
}

// Result is what a successful probe observed.
type Result struct {
	Version string
	Extra   map[string]string
}

// Protocol connects to a server over its wire protocol and verifies it responds.
type Protocol interface {
	Name() string     // canonical type token, e.g. "mysql"
	DefaultPort() int // used when the config omits a port
	// RequiresUser reports whether a user is mandatory. SQL servers need one;
	// redis allows password-only (legacy requirepass) or no auth at all.
	RequiresUser() bool
	Probe(ctx context.Context, cfg Config) (Result, error)
}

// registry maps protocol names (canonical and aliases) to protocols.
type registry struct {
	mu        sync.RWMutex
	byName    map[string]Protocol
	canonical map[string]bool
}

func newRegistry() *registry {
	return &registry{byName: map[string]Protocol{}, canonical: map[string]bool{}}
}

func (r *registry) register(p Protocol, aliases ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[p.Name()] = p
	r.canonical[p.Name()] = true
	for _, a := range aliases {
		r.byName[a] = p
	}
}

func (r *registry) lookup(name string) (Protocol, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byName[name]
	return p, ok
}

func (r *registry) names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.canonical))
	for n := range r.canonical {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// defaultRegistry holds the protocols compiled into the binary.
var defaultRegistry = newRegistry()

// Register adds a protocol (and optional aliases) to the default registry.
func Register(p Protocol, aliases ...string) { defaultRegistry.register(p, aliases...) }

// Lookup returns the protocol registered under name (canonical or alias).
func Lookup(name string) (Protocol, bool) { return defaultRegistry.lookup(name) }

// Names returns the registered canonical protocol names, sorted.
func Names() []string { return defaultRegistry.names() }
