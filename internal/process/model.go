// Package process discovers the processes that belong to a service (section 21).
//
// Identity is security-critical because kill decisions depend on it: Exe is the
// resolved target of /proc/<pid>/exe (never argv[0]/cmdline), and the user match
// is on the real UID. An unresolvable or "(deleted)" exe never matches an exe
// selector, so an unidentifiable process is reported, never killed.
//
// The MVP reads /proc directly through the Reader interface (hermetic in tests)
// rather than pulling a procfs dependency, matching how internal/locks reads
// /proc and keeping precise control over the fail-safe exe semantics.
package process

// Process is a discovered process belonging to a service.
type Process struct {
	PID     int      `json:"pid"`
	PPID    int      `json:"ppid"`
	User    string   `json:"user,omitempty"`
	UID     uint32   `json:"uid"`
	Exe     string   `json:"exe,omitempty"`     // resolved /proc/<pid>/exe; empty if unresolvable
	ExeOK   bool     `json:"exe_resolved"`      // false when exe could not be trusted
	Cmdline []string `json:"cmdline,omitempty"` // informational only, never matched on
	Role    string   `json:"role,omitempty"`    // selector name, or "child" for tree members
	Source  string   `json:"source"`            // pidfile | command_match | child
}

// Selector kinds (section 21).
const (
	SelectorPidfile      = "pidfile"
	SelectorCommandMatch = "command_match"
)

// Discovery source labels.
const (
	sourceBackend = "backend"
	sourcePidfile = "pidfile"
	sourceCommand = "command_match"
	sourceChild   = "child"
)

// Selector is one entry of a service's `processes` section.
type Selector struct {
	Name  string   // the map key, used as the discovered process Role
	Type  string   // pidfile | command_match
	Paths []string // pidfile: candidate paths, tried in order (first running pid wins)
	Exe   string   // command_match
	User  string   // command_match
}

// Identity is the raw per-process data read from /proc. ExeOK is false when the
// exe symlink could not be read or resolved to a real path (e.g. "(deleted)").
type Identity struct {
	PID     int
	PPID    int
	UID     uint32
	User    string
	Exe     string
	ExeOK   bool
	State   string // /proc/<pid>/stat run state: R, S, D, Z (zombie), ...
	Cmdline []string
}
