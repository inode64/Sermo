package servicemgr

import "strings"

const cgroupMembershipFields = 3

// CgroupOwnership reports whether procfs cgroup membership attributes a process
// to another unit. It is ClassifyCgroup's verdict read for one service: a
// service group is foreign unless it is unit's own; init, a transient scope and
// a user manager are always someone else's; root, slice and login-session
// membership is known but has no service owner. Unknown or malformed
// hierarchies never authorize deleted-executable cleanup.
func CgroupOwnership(content, unit string) (foreign, known bool) {
	owner := ClassifyCgroup(content)
	own := strings.TrimSuffix(unit, systemdServiceSuffix)
	switch owner.Class {
	case CgroupClassUnknown:
		return false, false
	case CgroupClassService:
		return owner.Owner != own, true
	case CgroupClassUserManager:
		return cgroupUserManagerPrefix+owner.Owner != own, true
	case CgroupClassInit, CgroupClassScope:
		return true, true
	case CgroupClassRoot, CgroupClassSlice, CgroupClassSession:
		return false, true
	}
	return false, true
}
