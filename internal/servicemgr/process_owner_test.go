package servicemgr

import "testing"

func TestCgroupOwnership(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, content  string
		foreign, known bool
	}{
		{"own unit", "0::/system.slice/squid.service/worker", false, true},
		{"another unit", "0::/system.slice/other.service", true, true},
		{"session", "0::/user.slice/user-0.slice/session-23.scope", false, true},
		{"openrc", "0::/openrc.squid", false, true},
		{"foreign openrc", "0::/openrc.other", true, true},
		{"root", "0::/", false, true},
		{"v1", "2:cpu:/\n1:name=systemd:/system.slice/squid.service", false, true},
		{"container scope", "0::/machine.slice/container.scope", true, true},
		{"malformed", "0::/system.slice/../squid.service", false, false},
		{"relative", "0::squid.service", false, false},
		{"unknown", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			foreign, known := CgroupOwnership(tc.content, "squid")
			if foreign != tc.foreign || known != tc.known {
				t.Fatalf("foreign=%v known=%v", foreign, known)
			}
		})
	}
}
