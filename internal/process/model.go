// Package process discovers the processes that belong to a service.
//
// Identity is security-critical because kill decisions depend on it: Exe is the
// resolved target of /proc/<pid>/exe, and the user match is on the real UID.
// Cmdline may be used only by an explicit process cmd selector to narrow
// discovery for shared binaries; it never authorizes signaling. An unresolvable
// or "(deleted)" exe never matches an exe selector, so an unidentifiable process
// is reported, never killed.
//
// Reads /proc directly through the Reader interface (hermetic in tests)
// rather than pulling a procfs dependency, matching how internal/locks reads
// /proc and keeping precise control over the fail-safe exe semantics.
package process

import "regexp"

// Process is a discovered process belonging to a service.
type Process struct {
	PID     int      `json:"pid"`
	PPID    int      `json:"ppid"`
	User    string   `json:"user,omitempty"`
	UID     uint32   `json:"uid"`
	Exe     string   `json:"exe,omitempty"`     // resolved /proc/<pid>/exe; empty if unresolvable
	ExeOK   bool     `json:"exe_resolved"`      // false when exe could not be trusted
	Cmdline []string `json:"cmdline,omitempty"` // display data; an explicit process cmd may filter on it
	Role    string   `json:"role,omitempty"`    // selector name, "main" for backend seeds, or "child" for tree members
	Source  string   `json:"source"`            // backend | pidfile | command_match | child
}

// Selector kinds.
const (
	SelectorPidfile      = "pidfile"
	SelectorCommandMatch = "command_match"
)

// Resolved service-tree process discovery keys.
const (
	// ServiceKeyPidfile is the top-level pidfile field parsed into a pidfile selector.
	ServiceKeyPidfile = "pidfile"
	// ServiceKeyPidfiles is the named pidfile roles field parsed into pidfile selectors.
	ServiceKeyPidfiles = "pidfiles"
	// SectionProcesses is the public block of named command-match selectors.
	SectionProcesses = "processes"
)

// Selector field keys.
const (
	// SelectorKeyExe is a process selector's exact executable path field.
	SelectorKeyExe = "exe"
	// SelectorKeyCmd is a process selector's command-line regex field.
	SelectorKeyCmd = "cmd"
	// SelectorKeyUser is a process selector's real-user field.
	SelectorKeyUser = "user"
	// SelectorKeyGroup is a process selector's real-group field.
	SelectorKeyGroup = "group"
)

// Stop policy field keys parsed by process kill policy.
const (
	// SectionStopPolicy is the block governing stop-time cleanup and signal escalation.
	SectionStopPolicy = "stop_policy"
	// StopPolicyKeyGracefulTimeout is stop_policy.graceful_timeout.
	StopPolicyKeyGracefulTimeout = "graceful_timeout"
	// StopPolicyKeyTermTimeout is stop_policy.term_timeout.
	StopPolicyKeyTermTimeout = "term_timeout"
	// StopPolicyKeyKillTimeout is stop_policy.kill_timeout.
	StopPolicyKeyKillTimeout = "kill_timeout"
	// StopPolicyKeyForceKill is stop_policy.force_kill.
	StopPolicyKeyForceKill = "force_kill"
	// StopPolicyKeyKillOnlyIf is stop_policy.kill_only_if.
	StopPolicyKeyKillOnlyIf = "kill_only_if"
	// StopPolicyKeyUsers is stop_policy.kill_only_if.users.
	StopPolicyKeyUsers = "users"
	// StopPolicyKeyExeAny is stop_policy.kill_only_if.exe_any.
	StopPolicyKeyExeAny = "exe_any"
)

// Discovery source labels.
const (
	sourceBackend = "backend"
	sourcePidfile = "pidfile"
	sourceCommand = "command_match"
	sourceChild   = "child"
)

// Process Role labels for non-selector members: backend seeds are the service's
// main process, and tree members discovered via PPID are children.
const (
	// RoleMain is the canonical role for the primary service process selector.
	RoleMain = "main"
	// RoleChild marks descendant processes discovered from a primary process.
	RoleChild = "child"
)

// Selector is one internal process discovery source. Public `processes` entries
// are command-match selectors; pidfile selectors are derived from top-level
// service `pidfile:` or per-role `pidfiles:`.
type Selector struct {
	Name  string   // selector key used as Role when matched; backend seeds use Role "main"
	Type  string   // pidfile | command_match
	Paths []string // pidfile: candidate paths, tried in order (first running pid wins)
	Exe   string   // exact /proc/<pid>/exe
	Cmd   string   // RE2 regex matched against the joined cmdline (argv)
	User  string   // real UID owner
	Group string   // real GID owner

	cmdRe   *regexp.Regexp // compiled Cmd, set by ParseSelectors; matches compiles lazily if empty
	exePath string         // canonicalized Exe, set by ParseSelectors; matches canonicalizes lazily if empty
}

// Identity is the raw per-process data read from /proc. ExeOK is false when the
// exe symlink could not be read or resolved to a real path (e.g. "(deleted)").
type Identity struct {
	PID     int
	PPID    int
	UID     uint32
	GID     uint32
	User    string
	Exe     string
	ExeOK   bool
	State   string // /proc/<pid>/stat run state: R, S, D, Z (zombie), ...
	Cmdline []string
}
