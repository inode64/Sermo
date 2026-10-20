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
		{"init", "0::/init.scope", true, true},
		{"user manager", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/squid.service", true, true},
		{"bare slice", "0::/system.slice", false, true},
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

func TestCgroupOwnedBy(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, content string
		owned         bool
	}{
		{"own unit", "0::/system.slice/squid.service/worker", true},
		{"own unit with suffix", "0::/system.slice/squid.service", true},
		{"openrc", "0::/openrc.squid", true},
		{"another unit", "0::/system.slice/other.service", false},
		{"foreign openrc", "0::/openrc.other", false},
		{"session", "0::/user.slice/user-0.slice/session-23.scope", false},
		{"user manager", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/squid.service", false},
		{"vm scope", "0::/machine.slice/machine-qemu.scope", false},
		{"root", "0::/", false},
		{"malformed", "0::/system.slice/../squid.service", false},
		{"unknown", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, unit := range []string{"squid", "squid.service"} {
				if got := CgroupOwnedBy(tc.content, unit); got != tc.owned {
					t.Fatalf("CgroupOwnedBy(%q, %q) = %v, want %v", tc.content, unit, got, tc.owned)
				}
			}
		})
	}
}
