package checks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func addrNetCheck(t *testing.T, expect string, samples ...[]string) *netCheck {
	t.Helper()
	i := 0
	return &netCheck{
		name: "net", timeout: time.Second,
		iface:  "ppp0",
		metric: "address",
		expect: expect,
		sampler: func(iface string) (NetSample, error) {
			s := NetSample{State: "up", Addrs: samples[min(i, len(samples)-1)], AddrsKnown: true}
			i++
			return s, nil
		},
	}
}

func TestNetAddressExpect(t *testing.T) {
	c := addrNetCheck(t, "present", []string{"203.0.113.7"})
	if r := c.Run(context.Background()); !r.OK || !strings.Contains(r.Message, "203.0.113.7") {
		t.Fatalf("present with address: %+v", r)
	}
	c = addrNetCheck(t, "present", nil)
	if r := c.Run(context.Background()); r.OK || !strings.Contains(r.Message, "none (want present)") {
		t.Fatalf("present without address must not hold: %+v", r)
	}
	c = addrNetCheck(t, "absent", nil)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("absent without address must hold: %+v", r)
	}
}

func TestNetAddressOnChange(t *testing.T) {
	c := addrNetCheck(t, "", []string{"203.0.113.7"}, []string{"203.0.113.7"}, []string{"198.51.100.9"})
	if r := c.Run(context.Background()); r.OK || !strings.Contains(r.Message, "baseline") {
		t.Fatalf("first run must prime, not fire: %+v", r)
	}
	if r := c.Run(context.Background()); r.OK {
		t.Fatalf("unchanged address must not fire: %+v", r)
	}
	r := c.Run(context.Background())
	if !r.OK || !strings.Contains(r.Message, "203.0.113.7->198.51.100.9") {
		t.Fatalf("renumbering must fire with old->new: %+v", r)
	}
	if r.Data["old"] != "203.0.113.7" || r.Data["new"] != "198.51.100.9" {
		t.Fatalf("data = %v, want old/new addresses", r.Data)
	}
}

// When netlink cannot list addresses (restricted container, transient
// failure) the sample has none, which must not read as "no address".
func TestNetAddressUnknownIsUnavailable(t *testing.T) {
	known := true
	c := &netCheck{
		name: "net", timeout: time.Second, iface: "ppp0", metric: "address",
		sampler: func(string) (NetSample, error) {
			if known {
				return NetSample{State: "up", Addrs: []string{"203.0.113.7"}, AddrsKnown: true}, nil
			}
			return NetSample{State: "up"}, nil
		},
	}
	if r := c.Run(t.Context()); r.OK || r.Unavailable {
		t.Fatalf("first run must prime: %+v", r)
	}
	known = false
	if r := c.Run(t.Context()); r.OK || !r.Unavailable {
		t.Fatalf("unreadable addresses must be unavailable, not a change: %+v", r)
	}
	known = true
	if r := c.Run(t.Context()); r.OK || r.Unavailable {
		t.Fatalf("the baseline must survive an unavailable sample: %+v", r)
	}
	c = &netCheck{
		name: "net", timeout: time.Second, iface: "ppp0", metric: "address", expect: NetAddrAbsent,
		sampler: func(string) (NetSample, error) { return NetSample{State: "up"}, nil },
	}
	if r := c.Run(t.Context()); r.OK || !r.Unavailable {
		t.Fatalf("expect absent must not fire on unreadable addresses: %+v", r)
	}
}

func TestBuildNetAddress(t *testing.T) {
	if _, warn := buildNetCheck(base{}, map[string]any{"interface": "ppp0", "metric": "address"}, Deps{}); !strings.Contains(warn, "expect: present|absent or on: change") {
		t.Fatalf("missing condition must warn, got %q", warn)
	}
	if _, warn := buildNetCheck(base{}, map[string]any{"interface": "ppp0", "metric": "address", "expect": "up"}, Deps{}); !strings.Contains(warn, "present or absent") {
		t.Fatalf("bad expect must warn, got %q", warn)
	}
	if _, warn := buildNetCheck(base{}, map[string]any{"interface": "ppp0", "metric": "address", "on": "change"}, Deps{}); warn != "" {
		t.Fatalf("on: change must build, got %q", warn)
	}
}
