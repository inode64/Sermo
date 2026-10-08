package servicemgr

import (
	"os"
	"path/filepath"
	"strings"
)

// CgroupClass names the init-level owner of a process's control group, as the
// kernel attributes it. It is observation only: no class authorizes a signal.
type CgroupClass string

const (
	// CgroupClassUnknown is an unreadable or malformed record, or a v1 host
	// without a name=systemd hierarchy: ownership cannot be told.
	CgroupClassUnknown CgroupClass = "unknown"
	// CgroupClassRoot is the root control group: outside every unit.
	CgroupClassRoot CgroupClass = "root"
	// CgroupClassInit is init.scope (PID 1 and its helpers).
	CgroupClassInit CgroupClass = "init"
	// CgroupClassService is a systemd .service unit or an OpenRC service group;
	// Owner is the unit name without its suffix.
	CgroupClassService CgroupClass = "service"
	// CgroupClassScope is a transient non-session scope (a container, a VM, a
	// systemd-run unit); Owner is the scope name without its suffix.
	CgroupClassScope CgroupClass = "scope"
	// CgroupClassUserManager is a user@N.service manager and everything it runs;
	// Owner is the numeric user id.
	CgroupClassUserManager CgroupClass = "user-manager"
	// CgroupClassSession is a logind session-N.scope; Owner is the session id.
	CgroupClassSession CgroupClass = "session"
	// CgroupClassSlice is a path made only of slices: no unit owns it.
	CgroupClassSlice CgroupClass = "slice"
)

// CgroupOwner is the classification of one procfs cgroup record.
type CgroupOwner struct {
	Class CgroupClass
	Owner string
}

const (
	cgroupInitScope         = "init.scope"
	cgroupUserSlice         = "user.slice"
	cgroupSessionPrefix     = "session-"
	cgroupUserManagerPrefix = "user@"
	openRCCgroupPrefix      = "openrc."
	cgroupRecordPrefix      = "0"
	cgroupNamedSystemd      = "name=systemd"
)

// ProcfsCgroupPath extracts the init hierarchy path from /proc/<pid>/cgroup
// content: the cgroup v2 record ("0::/path") or, on a v1 host, the named
// systemd hierarchy ("N:name=systemd:/path"). ok is false when neither is
// present or the path is not a clean absolute path, so a malformed record can
// never be mistaken for the root.
func ProcfsCgroupPath(content string) (string, bool) {
	return cgroupRecordPath(content, true)
}

func cgroupRecordPath(content string, acceptNamed bool) (string, bool) {
	for line := range strings.SplitSeq(content, serviceOutputLineSeparator) {
		fields := strings.SplitN(strings.TrimSpace(line), ":", cgroupMembershipFields)
		if len(fields) != cgroupMembershipFields {
			continue
		}
		unified := fields[0] == cgroupRecordPrefix && fields[1] == ""
		if !unified && !(acceptNamed && fields[1] == cgroupNamedSystemd) {
			continue
		}
		path := fields[2]
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", false
		}
		return path, true
	}
	return "", false
}

// ClassifyCgroup names which init unit, if any, owns the control group a
// procfs cgroup record describes. Components are read in order so a unit nested
// in a slice is found, and anything under user.slice is session territory even
// when it ends in a .service (the user manager and the units it runs).
func ClassifyCgroup(content string) CgroupOwner {
	path, ok := ProcfsCgroupPath(content)
	if !ok {
		return CgroupOwner{Class: CgroupClassUnknown}
	}
	if path == "/" {
		return CgroupOwner{Class: CgroupClassRoot}
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if parts[0] == cgroupUserSlice {
		return classifyUserSlice(parts[1:])
	}
	for _, part := range parts {
		switch {
		case part == cgroupInitScope:
			return CgroupOwner{Class: CgroupClassInit}
		case strings.HasSuffix(part, systemdServiceSuffix):
			return CgroupOwner{Class: CgroupClassService, Owner: strings.TrimSuffix(part, systemdServiceSuffix)}
		case strings.HasPrefix(part, openRCCgroupPrefix):
			return CgroupOwner{Class: CgroupClassService, Owner: strings.TrimPrefix(part, openRCCgroupPrefix)}
		case strings.HasSuffix(part, systemdScopeSuffix):
			name := strings.TrimSuffix(part, systemdScopeSuffix)
			if session, found := strings.CutPrefix(name, cgroupSessionPrefix); found {
				return CgroupOwner{Class: CgroupClassSession, Owner: session}
			}
			return CgroupOwner{Class: CgroupClassScope, Owner: name}
		}
	}
	return CgroupOwner{Class: CgroupClassSlice}
}

func classifyUserSlice(parts []string) CgroupOwner {
	for _, part := range parts {
		if name, found := strings.CutSuffix(part, systemdScopeSuffix); found {
			if session, isSession := strings.CutPrefix(name, cgroupSessionPrefix); isSession {
				return CgroupOwner{Class: CgroupClassSession, Owner: session}
			}
			return CgroupOwner{Class: CgroupClassScope, Owner: name}
		}
		if name, found := strings.CutSuffix(part, systemdServiceSuffix); found && strings.HasPrefix(name, cgroupUserManagerPrefix) {
			return CgroupOwner{Class: CgroupClassUserManager, Owner: strings.TrimPrefix(name, cgroupUserManagerPrefix)}
		}
	}
	return CgroupOwner{Class: CgroupClassSlice}
}

// OpenRCUnitCgroupsPresent reports whether the host's cgroup root holds at
// least one per-service OpenRC group (rc_cgroup_mode=unified). Without one every
// OpenRC process sits in the root group and root membership proves nothing.
// readDir defaults to os.ReadDir on cgroupRoot.
func OpenRCUnitCgroupsPresent(readDir func(string) ([]os.DirEntry, error)) bool {
	if readDir == nil {
		readDir = os.ReadDir
	}
	entries, err := readDir(cgroupRoot)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), openRCCgroupPrefix) {
			return true
		}
	}
	return false
}
