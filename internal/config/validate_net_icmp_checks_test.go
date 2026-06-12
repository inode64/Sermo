package config

import "testing"

// TestValidateSingleShotNetICMPSwapValid covers the single-metric forms of the
// net/icmp/swap checks inside a service's checks: section (unified check
// types): valid shapes must produce no issue for those checks.
func TestValidateSingleShotNetICMPSwapValid(t *testing.T) {
	issues := validateService(t, `
kind: service
name: svc
service: { name: x }
checks:
  link: { type: net, interface: ppp0, metric: state, expect: up }
  errs: { type: net, interface: eth0, metric: errors, delta: { op: ">", value: 100 } }
  ping: { type: icmp, host: 1.1.1.1, interface: ppp0, metric: state, expect: up }
  lat: { type: icmp, host: 1.1.1.1, metric: latency, threshold: { op: ">", value: 100 } }
  swp: { type: swap, metric: usage, used_pct: { op: ">=", value: 80 } }
  ip: { type: net, interface: ppp0, metric: address, expect: present }
  rt: { type: route, interface: ppp0 }
  rt6: { type: route, family: ipv6 }
  res: { type: dns, resolvconf: true, query: example.com, expect: { rcode: NOERROR } }
`)
	for _, name := range []string{"checks.link", "checks.errs", "checks.ping", "checks.lat", "checks.swp", "checks.ip", "checks.rt", "checks.rt6", "checks.res"} {
		if hasIssue(issues, name) {
			t.Fatalf("valid %s must produce no issue: %v", name, issues)
		}
	}
}

// TestValidateSingleShotNetICMPSwapErrors mirrors each builder requirement as
// a validation issue, so a broken single-shot net/icmp/swap check is reported
// at config validation instead of surfacing only at runtime.
func TestValidateSingleShotNetICMPSwapErrors(t *testing.T) {
	cases := map[string]struct {
		body string
		want string
	}{
		"net missing interface":        {`c: { type: net, metric: state, expect: up }`, "interface is required"},
		"net bad metric":               {`c: { type: net, interface: ppp0, metric: nope }`, "not a supported net metric"},
		"net state missing condition":  {`c: { type: net, interface: ppp0, metric: state }`, "requires expect: up|down or on: change"},
		"net errors missing delta":     {`c: { type: net, interface: eth0, metric: errors }`, "delta {op, value} is required"},
		"icmp missing host":            {`c: { type: icmp, metric: state, expect: up }`, "host is required"},
		"icmp bad count":               {`c: { type: icmp, host: 1.1.1.1, count: 0, metric: state, expect: up }`, "count must be a positive integer"},
		"icmp bad metric":              {`c: { type: icmp, host: 1.1.1.1, metric: nope }`, "not a supported icmp metric"},
		"icmp latency needs condition": {`c: { type: icmp, host: 1.1.1.1, metric: latency }`, "requires threshold {op, value} or change {delta}"},
		"swap bad metric":              {`c: { type: swap, metric: nope }`, "not a supported swap metric"},
		"address missing condition":    {`c: { type: net, interface: ppp0, metric: address }`, "requires expect: present|absent or on: change"},
		"address bad expect":           {`c: { type: net, interface: ppp0, metric: address, expect: up }`, "expect must be present or absent"},
		"route bad family":             {`c: { type: route, family: ipx }`, "family must be ipv4 or ipv6"},
		"route list interface":         {`c: { type: route, interface: [ppp0, eth0] }`, "single interface name"},
		"dns resolvconf not bool":      {`c: { type: dns, resolvconf: si }`, "resolvconf must be a boolean"},
		"dns resolvconf plus host":     {`c: { type: dns, host: 1.1.1.1, resolvconf: true }`, "host and resolvconf are mutually exclusive"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			mustHave(t, validateService(t, "kind: service\nname: svc\nservice: { name: x }\nchecks:\n  "+c.body+"\n"), c.want)
		})
	}
}
