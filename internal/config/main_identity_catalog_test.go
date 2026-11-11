package config

import (
	"maps"
	"slices"
	"testing"

	"sermo/internal/cfgval"
	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

// A daemon's main identity is where its init system and distribution put it:
// rspamd keeps a root main process under an init script and runs entirely as
// its own user under the packaged systemd unit, and Debian drops rpcbind and
// snmpd to system users. A profile that names one of them blocks every
// operation on the hosts that have the other.
func TestCatalogMainIdentityFollowsInitAndDistribution(t *testing.T) {
	for _, tc := range []struct {
		service, backend, os string
		selector, user, cmd  string
		absent               string
	}{
		{service: "rspamd", backend: string(servicemgr.BackendOpenRC), selector: process.RoleMain, user: "root", absent: "main-systemd"},
		{service: "rspamd", backend: string(servicemgr.BackendSystemd), selector: "main-systemd", user: "rspamd", cmd: "^rspamd: main process", absent: process.RoleMain},
		{service: "rpcbind", backend: string(servicemgr.BackendSystemd), os: "gentoo", selector: process.RoleMain, user: "root"},
		{service: "rpcbind", backend: string(servicemgr.BackendSystemd), os: "debian", selector: process.RoleMain, user: "_rpc"},
		{service: "snmpd", backend: string(servicemgr.BackendSystemd), os: "gentoo", selector: process.RoleMain, user: "root"},
		{service: "snmpd", backend: string(servicemgr.BackendSystemd), os: "debian", selector: process.RoleMain, user: "Debian-snmp"},
	} {
		t.Run(tc.service+"/"+tc.backend+"/"+tc.os, func(t *testing.T) {
			if tc.os != "" {
				old := detectedOS
				detectedOS = tc.os
				t.Cleanup(func() { detectedOS = old })
			}
			processes := nested(t, resolveCatalogService(t, tc.service, tc.backend).Tree, process.SectionProcesses)
			selector, ok := processes[tc.selector].(map[string]any)
			if !ok {
				t.Fatalf("selectors = %v, want %q", slices.Sorted(maps.Keys(processes)), tc.selector)
			}
			if got := cfgval.String(selector["user"]); got != tc.user {
				t.Fatalf("%s user = %q, want %q", tc.selector, got, tc.user)
			}
			if got := cfgval.String(selector["cmd"]); got != tc.cmd {
				t.Fatalf("%s cmd = %q, want %q", tc.selector, got, tc.cmd)
			}
			if _, present := processes[tc.absent]; tc.absent != "" && present {
				t.Fatalf("selectors = %v, want no %q under %s", slices.Sorted(maps.Keys(processes)), tc.absent, tc.backend)
			}
		})
	}
}
