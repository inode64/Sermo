package process

import "testing"

func TestStaleBinariesDoesNotRecommendRestartForStrays(t *testing.T) {
	d := Discoverer{}
	got := d.StaleBinariesIn([]Process{
		{PID: 90, Source: SourceBackend, ExeOK: true, Exe: "/usr/bin/sshd"},
		{PID: 301, Source: SourceBackend, Stray: true, ExePrev: "/usr/bin/mariadb"},
		{PID: 302, Source: SourceBackend, Delegated: true, Kept: true, ExePrev: "/usr/bin/zsh"},
	}, nil)
	if len(got) != 0 {
		t.Fatalf("stray or kept child requested service restart: %v", got)
	}
}

// A guest's qemu or a network's dnsmasq is the service's own target: its replaced
// binary is exactly what the operator needs to hear about. The kept child above
// is workload, which the daemon's restart would not renew anyway.
func TestStaleBinariesReportsSelectorDelegatedProcess(t *testing.T) {
	d := Discoverer{}
	got := d.StaleBinariesIn([]Process{
		{PID: 400, Source: SelectorCommandMatch, Delegated: true, ExePrev: "/usr/bin/qemu-system-x86_64"},
	}, nil)
	if len(got) != 1 || got[0].PID != 400 {
		t.Fatalf("stale = %+v, want the delegated qemu", got)
	}
}

// Discovery attributes the process to its delegated selector, so the report goes
// through attribution rather than the unattributed fallback, once per process.
func TestStaleBinariesReportsDelegatedSelectorThroughDiscovery(t *testing.T) {
	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			301: {PID: 301, UID: 0, ExePrev: "/usr/bin/dnsmasq"},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0}),
	}
	selector := staleSelector("dnsmasq", "/usr/bin/dnsmasq", "root")
	selector.Delegated = true
	if got := d.StaleBinaries([]Selector{selector}); len(got) != 1 || got[0].PID != 301 {
		t.Fatalf("stale = %+v, want the delegated process once", got)
	}
}
