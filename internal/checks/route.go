package checks

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// DefaultRoute is one up default-route entry: the egress interface and the
// gateway address ("" when the route has no gateway, as on point-to-point
// links like PPP, where the device itself is the next hop).
type DefaultRoute struct {
	Iface   string
	Gateway string
}

// RouteSamplerFunc lists the kernel's up default routes for an address family
// ("ipv4" or "ipv6"). Injected for tests; the default reads /proc/net/route
// and /proc/net/ipv6_route.
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
	matched := routes
	if c.iface != "" {
		matched = nil
		for _, r := range routes {
			if r.Iface == c.iface {
				matched = append(matched, r)
			}
		}
	}
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
	res.Data = map[string]any{"family": c.family, "routes": len(routes), "value": len(matched)}
	if ok {
		res.Data["interface"] = matched[0].Iface
		if matched[0].Gateway != "" {
			res.Data["gateway"] = matched[0].Gateway
		}
	}
	return res
}

// defaultRouteSampler reads the kernel routing tables from procfs.
func defaultRouteSampler(family string) ([]DefaultRoute, error) {
	if family == "ipv6" {
		data, err := os.ReadFile("/proc/net/ipv6_route")
		if err != nil {
			return nil, err
		}
		return parseIPv6RouteTable(string(data)), nil
	}
	data, err := os.ReadFile("/proc/net/route")
	if err != nil {
		return nil, err
	}
	return parseRouteTable(string(data)), nil
}

// rtfUp is the kernel RTF_UP route flag (the route is usable).
const rtfUp = 0x1

// parseRouteTable extracts the up IPv4 default routes from /proc/net/route
// (header line, then per-route: Iface Destination Gateway Flags ... Mask ...;
// addresses are little-endian hex words). Default = destination and mask both
// zero.
func parseRouteTable(data string) []DefaultRoute {
	var out []DefaultRoute
	for i, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if i == 0 || len(fields) < 8 {
			continue // header or malformed
		}
		flags, err := strconv.ParseUint(fields[3], 16, 32)
		if err != nil || flags&rtfUp == 0 {
			continue
		}
		if fields[1] != "00000000" || fields[7] != "00000000" {
			continue // not the default route
		}
		out = append(out, DefaultRoute{Iface: fields[0], Gateway: hexLEIPv4(fields[2])})
	}
	return out
}

// parseIPv6RouteTable extracts the up IPv6 default routes from
// /proc/net/ipv6_route (per-route: dest dest-prefix src src-prefix next-hop
// metric refcnt use flags device; addresses are big-endian hex). Default =
// destination prefix zero; loopback-device entries (the kernel's fallback
// reject routes) are skipped.
func parseIPv6RouteTable(data string) []DefaultRoute {
	var out []DefaultRoute
	for _, line := range strings.Split(data, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		flags, err := strconv.ParseUint(fields[8], 16, 32)
		if err != nil || flags&rtfUp == 0 {
			continue
		}
		if fields[1] != "00" || fields[9] == "lo" {
			continue
		}
		out = append(out, DefaultRoute{Iface: fields[9], Gateway: hexBEIPv6(fields[4])})
	}
	return out
}

// hexLEIPv4 renders a little-endian hex word from /proc/net/route as a dotted
// IPv4 address, or "" for zero (no gateway).
func hexLEIPv4(s string) string {
	n, err := strconv.ParseUint(s, 16, 32)
	if err != nil || n == 0 {
		return ""
	}
	return net.IPv4(byte(n), byte(n>>8), byte(n>>16), byte(n>>24)).String()
}

// hexBEIPv6 renders a 32-digit big-endian hex address from
// /proc/net/ipv6_route as an IPv6 address, or "" for zero (no next hop).
func hexBEIPv6(s string) string {
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != net.IPv6len {
		return ""
	}
	ip := net.IP(raw)
	if ip.IsUnspecified() {
		return ""
	}
	return ip.String()
}
