package servicemgr

import "strings"

const cgroupMembershipFields = 3

// CgroupOwnership reports whether procfs cgroup membership attributes a process
// to another unit. It is ClassifyCgroup's verdict read for one service: a
// service group is foreign unless it is unit's own; init, a transient scope and
// a user manager are always someone else's; root, slice and login-session
// membership is known but has no service owner. Unknown or malformed
// hierarchies never authorize deleted-executable cleanup. An empty unit owns no
// group, so every service group is foreign to it.
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
	case CgroupClassRoot, CgroupClassSlice, CgroupClassSession:
		return false, true
	}
	return false, true
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
