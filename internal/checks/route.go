package checks

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/vishvananda/netlink"
)

// Address family selectors for a route check.
const (
	// FamilyIPv4 and FamilyIPv6 are the `family:` values of a route check;
	// exported so config validation checks the same set the check accepts.
	FamilyIPv4 = "ipv4"
	FamilyIPv6 = "ipv6"
	// RouteFamilySummary is the user-facing list of route family selectors.
	RouteFamilySummary = FamilyIPv4 + " or " + FamilyIPv6
)

// DefaultRoute is one up default-route entry: the egress interface and the
// gateway address ("" when the route has no gateway, as on point-to-point
// links like PPP, where the device itself is the next hop).
type DefaultRoute struct {
	Iface   string
	Gateway string
}

// RouteSamplerFunc lists the kernel's up default routes for an address family
// ("ipv4" or "ipv6"). Injected for tests; the default reads routes through
// netlink.
type RouteSamplerFunc func(family string) ([]DefaultRoute, error)

// routeCheck verifies the host has an up default route — optionally egressing
// through a specific interface. It is a health check (OK==true is healthy):
// as a watch it fires when the route disappears. It closes the gap the link
// and ping layers leave on uplinks: after a failed PPP renegotiation the
// interface can stay up with the default route gone, and a ping bound to the
// interface cannot tell "no route" from "provider down".
type routeCheck struct {
	base
	family  string // ipv4 | ipv6
	iface   string // optional: a default route must egress through it
	sampler RouteSamplerFunc
}

func (c routeCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultRouteSampler
	}
	routes, err := sampler(c.family)
	if err != nil {
		return c.result(false, "route: "+err.Error(), start)
	}
	matched := matchingRoutes(routes, c.iface)
	ok := len(matched) > 0
	var msg string
	switch {
	case ok && matched[0].Gateway != "":
		msg = fmt.Sprintf("%s default route via %s (gw %s)", c.family, matched[0].Iface, matched[0].Gateway)
	case ok:
		msg = fmt.Sprintf("%s default route via %s", c.family, matched[0].Iface)
	case c.iface != "" && len(routes) > 0:
		msg = fmt.Sprintf("no %s default route via %s (%d elsewhere)", c.family, c.iface, len(routes))
	default:
		msg = "no " + c.family + " default route"
	}
	res := c.result(ok, msg, start)
	res.Data = map[string]any{DataKeyFamily: c.family, DataKeyRoutes: len(routes), DataKeyValue: len(matched)}
	if ok {
		res.Data[DataKeyInterface] = matched[0].Iface
		if matched[0].Gateway != "" {
			res.Data[DataKeyGateway] = matched[0].Gateway
		}
	}
	return res
}

func matchingRoutes(routes []DefaultRoute, iface string) []DefaultRoute {
	if iface == "" {
		return routes
	}
	var matched []DefaultRoute
	for _, r := range routes {
		if r.Iface == iface {
			matched = append(matched, r)
		}
	}
	return matched
}

// Linux route type values from rtnetlink. netlink.Route.Type is 0 when unset in
// tests/builders, and the kernel reports unicast routes as 1.
const routeTypeUnicast = 1

// defaultRouteSampler reads the kernel routing tables through netlink.
func defaultRouteSampler(family string) ([]DefaultRoute, error) {
	nlFamily, err := netlinkFamily(family)
	if err != nil {
		return nil, err
	}

	links, err := netlink.LinkList()
	if err != nil {
		return nil, err
	}
	routes, err := netlink.RouteList(nil, nlFamily)
	if err != nil {
		return nil, err
	}
	return defaultRoutesFromNetlink(family, routes, netlinkLinkNames(links)), nil
}

// SampleRoutes returns one live default-route observation using the default
// netlink sampler.
func SampleRoutes(family string) ([]DefaultRoute, error) { return defaultRouteSampler(family) }

func netlinkFamily(family string) (int, error) {
	switch family {
	case FamilyIPv4, "":
		return netlink.FAMILY_V4, nil
	case FamilyIPv6:
		return netlink.FAMILY_V6, nil
	default:
		return 0, fmt.Errorf("unknown route family %q", family)
	}
}

func netlinkLinkNames(links []netlink.Link) map[int]string {
	names := make(map[int]string, len(links))
	for _, link := range links {
		if attrs := link.Attrs(); attrs != nil && attrs.Index > 0 && attrs.Name != "" {
			names[attrs.Index] = attrs.Name
		}
	}
	return names
}

func defaultRoutesFromNetlink(family string, routes []netlink.Route, linkNames map[int]string) []DefaultRoute {
	var out []DefaultRoute
	for i := range routes {
		if !isDefaultNetlinkRoute(family, routes[i]) {
			continue
		}
		if len(routes[i].MultiPath) > 0 {
			for _, hop := range routes[i].MultiPath {
				out = appendDefaultRoute(out, family, linkNames[hop.LinkIndex], hop.Gw)
			}
			continue
		}
		out = appendDefaultRoute(out, family, linkNames[routes[i].LinkIndex], routes[i].Gw)
	}
	return out
}

func isDefaultNetlinkRoute(family string, route netlink.Route) bool {
	if route.Type != 0 && route.Type != routeTypeUnicast {
		return false
	}
	if route.Dst == nil {
		return true
	}
	ones, bits := route.Dst.Mask.Size()
	if ones != 0 {
		return false
	}
	return (family == FamilyIPv6 && bits == 128) || (family != FamilyIPv6 && bits == 32)
}

func appendDefaultRoute(routes []DefaultRoute, family, iface string, gateway net.IP) []DefaultRoute {
	if iface == "" || (family == FamilyIPv6 && iface == "lo") {
		return routes
	}
	gw := ""
	if len(gateway) > 0 && !gateway.IsUnspecified() {
		gw = gateway.String()
	}
	return append(routes, DefaultRoute{Iface: iface, Gateway: gw})
}
