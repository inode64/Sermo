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

// Signal authority rests on CgroupOwnedBy, so a nameless unit, a colliding prefix
// or a sibling template instance must never read as the unit's own group.
func TestCgroupOwnedByEdgeCases(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, content, unit string
		owned               bool
	}{
		{"empty unit, nameless service group", "0::/system.slice/.service", "", false},
		{"empty unit, nameless openrc group", "0::/openrc.", "", false},
		{"bare suffix unit, nameless service group", "0::/system.slice/.service", ".service", false},
		{"empty unit, named group", "0::/system.slice/squid.service", "", false},
		{"systemd prefix collision", "0::/system.slice/squid-extra.service", "squid", false},
		{"openrc prefix collision", "0::/openrc.squid2", "squid", false},
		{"template instance is not the template", "0::/system.slice/system-squid.slice/squid@a.service", "squid", false},
		{"template instance owns itself", "0::/system.slice/system-squid.slice/squid@a.service", "squid@a", true},
		{"sibling template instance", "0::/system.slice/system-squid.slice/squid@b.service", "squid@a.service", false},
		{"init scope", "0::/init.scope", "squid", false},
		{"init scope, empty unit", "0::/init.scope", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := CgroupOwnedBy(tc.content, tc.unit); got != tc.owned {
				t.Fatalf("CgroupOwnedBy(%q, %q) = %v, want %v", tc.content, tc.unit, got, tc.owned)
			}
		})
	}
}

// An empty unit owns no group: every service and user-manager group is foreign
// to it, never its own.
func TestCgroupOwnershipEmptyUnit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, content  string
		foreign, known bool
	}{
		{"nameless service group", "0::/system.slice/.service", true, true},
		{"nameless openrc group", "0::/openrc.", true, true},
		{"named service group", "0::/system.slice/squid.service", true, true},
		{"user manager", "0::/user.slice/user-1000.slice/user@1000.service", true, true},
		{"session", "0::/user.slice/user-0.slice/session-23.scope", false, true},
		{"root", "0::/", false, true},
		{"unknown", "", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			for _, unit := range []string{"", ".service"} {
				foreign, known := CgroupOwnership(tc.content, unit)
				if foreign != tc.foreign || known != tc.known {
					t.Fatalf("CgroupOwnership(%q, %q) = foreign %v known %v", tc.content, unit, foreign, known)
				}
			}
		})
	}
}
