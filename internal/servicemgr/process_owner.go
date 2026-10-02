package servicemgr

import (
	"path/filepath"
	"strings"
)

const cgroupMembershipFields = 3

// CgroupOwnership reports whether procfs cgroup membership attributes a process
// to another unit. Root/session membership is known but has no service owner.
// Unknown or malformed hierarchies never authorize deleted-executable cleanup.
func CgroupOwnership(content, unit string) (foreign, known bool) {
	for line := range strings.SplitSeq(content, serviceOutputLineSeparator) {
		fields := strings.SplitN(line, ":", cgroupMembershipFields)
		if len(fields) != cgroupMembershipFields || (fields[0] != "0" || fields[1] != "") && fields[1] != "name=systemd" {
			continue
		}
		path := fields[2]
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return false, false
		}
		for part := range strings.SplitSeq(path, "/") {
			if owner, found := strings.CutSuffix(part, systemdServiceSuffix); found {
				return owner != strings.TrimSuffix(unit, systemdServiceSuffix), true
			}
			if owner, found := strings.CutPrefix(part, "openrc."); found {
				return owner != unit, true
			}
			if strings.HasSuffix(part, ".scope") && !strings.HasPrefix(part, "session-") {
				return true, true
			}
		}
		return false, true
	}
	return false, false
}
