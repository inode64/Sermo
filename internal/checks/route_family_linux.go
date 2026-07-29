//go:build linux

package checks

import "github.com/vishvananda/netlink"

// familyV4 and familyV6 are the netlink address-family selectors for the route
// query. On Linux they are the kernel constants netlink itself uses.
const (
	familyV4 = netlink.FAMILY_V4
	familyV6 = netlink.FAMILY_V6
)
