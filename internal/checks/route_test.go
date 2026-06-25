package checks

import (
	"context"
	"fmt"
	"net"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

const routeTypeBlackhole = 6

func TestDefaultRoutesFromNetlinkIPv4(t *testing.T) {
	default4 := mustCIDR(t, "0.0.0.0/0")
	lan := mustCIDR(t, "192.168.0.0/24")
	routes := []netlink.Route{
		{LinkIndex: 1, Dst: nil, Gw: net.ParseIP("192.168.1.1"), Type: routeTypeUnicast},
		{LinkIndex: 2, Dst: default4, Type: routeTypeUnicast},
		{LinkIndex: 2, Dst: lan, Type: routeTypeUnicast},
		{LinkIndex: 3, Dst: default4, Type: routeTypeBlackhole},
		{Dst: default4, MultiPath: []*netlink.NexthopInfo{
			{LinkIndex: 1, Gw: net.ParseIP("10.0.0.1")},
			{LinkIndex: 2},
		}},
	}
	got := defaultRoutesFromNetlink("ipv4", routes, map[int]string{1: "eth0", 2: "ppp0", 3: "blackhole"})
	want := []DefaultRoute{
		{Iface: "eth0", Gateway: "192.168.1.1"},
		{Iface: "ppp0"},
		{Iface: "eth0", Gateway: "10.0.0.1"},
		{Iface: "ppp0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %#v, want %#v", got, want)
	}
}

func TestDefaultRoutesFromNetlinkIPv6SkipsLoopback(t *testing.T) {
	default6 := mustCIDR(t, "::/0")
	routes := []netlink.Route{
		{LinkIndex: 1, Dst: default6, Type: routeTypeUnicast},
		{LinkIndex: 2, Dst: default6, Gw: net.ParseIP("fe80::211:22ff:fe33:4455"), Type: routeTypeUnicast},
	}
	got := defaultRoutesFromNetlink("ipv6", routes, map[int]string{1: "lo", 2: "eth0"})
	want := []DefaultRoute{{Iface: "eth0", Gateway: "fe80::211:22ff:fe33:4455"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes = %#v, want %#v", got, want)
	}
}

func TestNetlinkFamily(t *testing.T) {
	if family, err := netlinkFamily("ipv4"); err != nil || family != netlink.FAMILY_V4 {
		t.Fatalf("ipv4 family = %d, %v", family, err)
	}
	if family, err := netlinkFamily("ipv6"); err != nil || family != netlink.FAMILY_V6 {
		t.Fatalf("ipv6 family = %d, %v", family, err)
	}
	if _, err := netlinkFamily("ipx"); err == nil {
		t.Fatal("bad family should error")
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
	} else if _, has := r.Data["gateway"]; has {
		t.Fatalf("a gateway-less route must not carry a gateway field: %v", r.Data)
	}
	res = mk([]DefaultRoute{{Iface: "ppp0", Gateway: "10.0.0.1"}}, nil, "ppp0")
	if r := res.Run(context.Background()); !r.OK || !strings.Contains(r.Message, "gw 10.0.0.1") {
		t.Fatalf("interface match: %+v", r)
	} else if r.Data["gateway"] != "10.0.0.1" {
		t.Fatalf("data gateway = %v, want 10.0.0.1", r.Data["gateway"])
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

func mustCIDR(t *testing.T, s string) *net.IPNet {
	t.Helper()
	_, network, err := net.ParseCIDR(s)
	if err != nil {
		t.Fatal(err)
	}
	return network
}
