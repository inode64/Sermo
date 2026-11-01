package servicemgr

import "strings"

const cgroupMembershipFields = 3

// CgroupOwnership reports whether procfs cgroup membership attributes a process
// to another unit. It is ClassifyCgroup's verdict read for one service: a
// service group is foreign unless it is unit's own; init, a transient scope and
// a user manager are always someone else's; root, slice and login-session
// membership is known but has no service owner, and a group outside every slice
// is a container's. Unknown or malformed hierarchies never authorize
// deleted-executable cleanup. An empty unit owns no group, so every service group
// is foreign to it.
func CgroupOwnership(content, unit string) (foreign, known bool) {
	owner := ClassifyCgroup(content)
	own := unitOwner(unit)
	switch owner.Class {
	case CgroupClassUnknown:
		return false, false
	case CgroupClassService:
		return own == "" || owner.Owner != own, true
	case CgroupClassUserManager:
		return cgroupUserManagerPrefix+owner.Owner != own, true
	case CgroupClassInit, CgroupClassScope:
		return true, true
	case CgroupClassSlice:
		return outsideSlices(content), true
	case CgroupClassRoot, CgroupClassSession:
		return false, true
	}
	return false, true
}

// outsideSlices reports a group init did not make. A cgroupfs driver places a
// container at the top of the hierarchy (/docker/<id>, /kubepods/…, /lxc/<name>),
// and the exe link of a process in it reads as the guest sees it, so a host
// selector's exact path and UID match there without naming the host's daemon.
func outsideSlices(content string) bool {
	path, _ := ProcfsCgroupPath(content)
	first, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return notSlice(first)
}

// CgroupOwnedBy reports positive kernel evidence that a process belongs to
// unit's own service group: the systemd `<unit>.service` or OpenRC
// `openrc.<unit>` cgroup. Root, slice, session, scope and unknown membership
// never qualify, so a host without per-service cgroups proves nothing. An empty
// unit (or a bare `.service`) owns nothing: it never matches a nameless group
// such as `/system.slice/.service` or `/openrc.`.
func CgroupOwnedBy(content, unit string) bool {
	own := unitOwner(unit)
	if own == "" {
		return false
	}
	owner := ClassifyCgroup(content)
	return owner.Class == CgroupClassService && owner.Owner == own
}

// unitOwner is the owner a unit's own service group carries: the unit name
// without its .service suffix (OpenRC names carry none).
func unitOwner(unit string) string {
	return strings.TrimSuffix(unit, systemdServiceSuffix)
}
