// Package conn provides connection-protocol probes (MySQL/MariaDB, and future
// protocols) behind a small registry. Each protocol implements Protocol and
// registers itself; the checks package looks a protocol up by name to build a
// generic connection check. It is a leaf package: it depends on neither checks
// nor config, so both can import it without a cycle.
package conn

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"sort"
	"strings"
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
	// RequiresUser reports whether a user is mandatory. Some protocols can
	// prove liveness from an unauthenticated greeting; others need a user.
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

// readCRLFLine reads one CRLF/LF-terminated line, trimmed — the line shape
// every text protocol probe (redis RESP, imap, nut, …) reads.
func readCRLFLine(br *bufio.Reader) (string, error) {
	s, err := br.ReadString('\n')
	return strings.TrimRight(s, "\r\n"), err
}

// readGreetingLine reads one CR/LF-terminated greeting line from a fresh reader
// over r, trimmed. It tolerates a read error as long as some data arrived — a
// server that sends its banner then closes without a final newline — returning
// the error only when nothing was read. For single-line greetings; a probe that
// reads more lines must keep its own bufio.Reader.
func readGreetingLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// randXID32 returns a random 32-bit transaction id with a fixed fallback when
// the system RNG fails, shared by the rpcbind/nfs and dhcp probes.
func randXID32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0x53524d4f // "SRMO"
	}
	return binary.BigEndian.Uint32(b[:])
}
