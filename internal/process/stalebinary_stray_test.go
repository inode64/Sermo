package process

import "testing"

func TestStaleBinariesDoesNotRecommendRestartForStrays(t *testing.T) {
	d := Discoverer{}
	got := d.StaleBinariesIn([]Process{
		{PID: 90, Source: SourceBackend, ExeOK: true, Exe: "/usr/bin/sshd"},
		{PID: 301, Source: SourceBackend, Stray: true, ExePrev: "/usr/bin/mariadb"},
		{PID: 302, Source: SourceBackend, Delegated: true, ExePrev: "/usr/bin/zsh"},
	}, nil)
	if len(got) != 0 {
		t.Fatalf("stray requested service restart: %v", got)
	}
}

func TestStaleBinariesFallbackExcludesDelegatedSelectors(t *testing.T) {
	d := Discoverer{
		Reader: fakeReader{ids: map[int]Identity{
			301: {PID: 301, UID: 0, ExePrev: "/usr/bin/zsh"},
		}},
		ResolveUser: fakeUsers(map[string]uint32{"root": 0}),
	}
	selector := staleSelector("persistent-shell", "/usr/bin/zsh", "root")
	selector.Delegated = true
	if got := d.StaleBinaries([]Selector{selector}); len(got) != 0 {
		t.Fatalf("delegated process requested service restart: %v", got)
	}
}
