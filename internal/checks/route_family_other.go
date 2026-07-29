//go:build !linux

package checks

// familyV4 and familyV6 mirror netlink.FAMILY_V4 / FAMILY_V6 (AF_INET /
// AF_INET6) so off-Linux builds compile. The netlink route query is a no-op stub
// off Linux, so these values are never used against a live kernel.
const (
	familyV4 = 2  // AF_INET
	familyV6 = 10 // AF_INET6 on Linux
)
