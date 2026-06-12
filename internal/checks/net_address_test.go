package checks

import (
	"context"
	"strings"
	"testing"
	"time"
)

func addrNetCheck(t *testing.T, expect string, onChange bool, samples ...[]string) *netCheck {
	t.Helper()
	i := 0
	return &netCheck{
		base:     base{name: "net", timeout: time.Second},
		iface:    "ppp0",
		metric:   "address",
		expect:   expect,
		onChange: onChange,
		sampler: func(iface string) (NetSample, error) {
			s := NetSample{State: "up", Addrs: samples[min(i, len(samples)-1)]}
			i++
			return s, nil
		},
	}
}

func TestNetAddressExpect(t *testing.T) {
	c := addrNetCheck(t, "present", false, []string{"203.0.113.7"})
	if r := c.Run(context.Background()); !r.OK || !strings.Contains(r.Message, "203.0.113.7") {
		t.Fatalf("present with address: %+v", r)
	}
	c = addrNetCheck(t, "present", false, nil)
	if r := c.Run(context.Background()); r.OK || !strings.Contains(r.Message, "none (want present)") {
		t.Fatalf("present without address must not hold: %+v", r)
	}
	c = addrNetCheck(t, "absent", false, nil)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("absent without address must hold: %+v", r)
	}
}

func TestNetAddressOnChange(t *testing.T) {
	c := addrNetCheck(t, "", true, []string{"203.0.113.7"}, []string{"203.0.113.7"}, []string{"198.51.100.9"})
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
