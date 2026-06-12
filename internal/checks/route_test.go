package checks

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ipv4RouteFixture mirrors /proc/net/route on a host with a PPP uplink: an up
// gatewayless default via ppp0, a non-default LAN route, and a downed default
// (RTF_UP clear) that must be ignored.
const ipv4RouteFixture = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n" +
	"ppp0\t00000000\t00000000\t0001\t0\t0\t0\t00000000\t0\t0\t0\n" +
	"eth0\t0000A8C0\t00000000\t0001\t0\t0\t0\t00FFFFFF\t0\t0\t0\n" +
	"eth1\t00000000\t0101A8C0\t0002\t0\t0\t0\t00000000\t0\t0\t0\n"

func TestParseRouteTable(t *testing.T) {
	routes := parseRouteTable(ipv4RouteFixture)
	if len(routes) != 1 || routes[0].Iface != "ppp0" || routes[0].Gateway != "" {
		t.Fatalf("routes = %v, want one gatewayless default via ppp0", routes)
	}

	// A gatewayed default decodes the little-endian hex address.
	gw := "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\n" +
		"eth0\t00000000\t0101A8C0\t0003\t0\t0\t0\t00000000\t0\t0\t0\n"
	routes = parseRouteTable(gw)
	if len(routes) != 1 || routes[0].Gateway != "192.168.1.1" {
		t.Fatalf("routes = %v, want default via eth0 gw 192.168.1.1", routes)
	}
}

func TestParseIPv6RouteTable(t *testing.T) {
	zero32 := strings.Repeat("0", 32)
	gw := "fe80000000000000021122fffe334455"
	data := strings.Join([]string{
		// up default via eth0 with a link-local next hop
		zero32 + " 00 " + zero32 + " 00 " + gw + " 00000400 00000001 00000000 00000003 eth0",
		// kernel fallback reject route on lo — skipped
		zero32 + " 00 " + zero32 + " 00 " + zero32 + " ffffffff 00000001 00000000 00200200 lo",
		// non-default prefix — skipped
		"20010db8000000000000000000000000 40 " + zero32 + " 00 " + zero32 + " 00000100 00000001 00000000 00000001 eth0",
	}, "\n")
	routes := parseIPv6RouteTable(data)
	if len(routes) != 1 || routes[0].Iface != "eth0" || routes[0].Gateway != "fe80::211:22ff:fe33:4455" {
		t.Fatalf("routes = %v, want one default via eth0 with link-local gw", routes)
	}
}

func TestRouteCheckRun(t *testing.T) {
	mk := func(routes []DefaultRoute, err error, iface string) routeCheck {
		return routeCheck{
			base:   base{name: "route", timeout: time.Second},
			family: "ipv4",
			iface:  iface,
			sampler: func(family string) ([]DefaultRoute, error) {
				if family != "ipv4" {
					t.Fatalf("sampler family = %q, want ipv4", family)
				}
				return routes, err
			},
		}
	}

	res := mk([]DefaultRoute{{Iface: "ppp0"}}, nil, "")
	if r := res.Run(context.Background()); !r.OK || !strings.Contains(r.Message, "via ppp0") {
		t.Fatalf("present default: %+v", r)
	}
	res = mk([]DefaultRoute{{Iface: "ppp0", Gateway: "10.0.0.1"}}, nil, "ppp0")
	if r := res.Run(context.Background()); !r.OK || !strings.Contains(r.Message, "gw 10.0.0.1") {
		t.Fatalf("interface match: %+v", r)
	}
	res = mk([]DefaultRoute{{Iface: "eth0", Gateway: "192.168.1.1"}}, nil, "ppp0")
	if r := res.Run(context.Background()); r.OK || !strings.Contains(r.Message, "no ipv4 default route via ppp0 (1 elsewhere)") {
		t.Fatalf("wrong interface must fail: %+v", r)
	}
	res = mk(nil, nil, "")
	if r := res.Run(context.Background()); r.OK || !strings.Contains(r.Message, "no ipv4 default route") {
		t.Fatalf("no routes must fail: %+v", r)
	}
	res = mk(nil, fmt.Errorf("boom"), "")
	if r := res.Run(context.Background()); r.OK || !strings.Contains(r.Message, "boom") {
		t.Fatalf("sampler error must fail: %+v", r)
	}
}

func TestBuildRouteCheck(t *testing.T) {
	if _, warn := buildRouteCheck(base{}, map[string]any{"family": "ipx"}, Deps{}); warn == "" {
		t.Fatal("bad family must warn")
	}
	c, warn := buildRouteCheck(base{}, map[string]any{"interface": "ppp0"}, Deps{})
	if warn != "" {
		t.Fatalf("warn = %q", warn)
	}
	rc := c.(routeCheck)
	if rc.family != "ipv4" || rc.iface != "ppp0" {
		t.Fatalf("check = %+v, want ipv4/ppp0 (family defaults to ipv4)", rc)
	}
}
