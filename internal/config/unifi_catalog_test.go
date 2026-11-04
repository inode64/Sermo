package config

import (
	"testing"
	"time"

	"sermo/internal/process"
)

func TestCatalogUnifiStrictMainAndDatabaseStopWindows(t *testing.T) {
	for _, tc := range []struct {
		os, exe string
	}{
		{os: "debian", exe: "/usr/bin/java"},
		{os: "gentoo", exe: "/etc/java-config-2/current-system-vm/bin/java"},
	} {
		t.Run(tc.os, func(t *testing.T) {
			old := detectedOS
			detectedOS = tc.os
			t.Cleanup(func() { detectedOS = old })
			resolved := resolveCatalogService(t, "unifi", "openrc")
			selectors, warnings := process.ParseSelectors(resolved.Tree)
			if len(warnings) != 0 {
				t.Fatalf("ParseSelectors warnings = %v", warnings)
			}
			var main process.Selector
			for _, selector := range selectors {
				if selector.Name == process.RoleMain {
					main = selector
				}
				if selector.Name == "controller" {
					t.Fatal("the obsolete controller role must not remain alongside main")
				}
			}
			if !main.IsStrictPrincipal() || main.Exe != tc.exe || main.User != "unifi" || main.Cmd != "java .*unifi" {
				t.Fatalf("main cannot establish UniFi ownership for deleted MongoDB cleanup: %+v", main)
			}
			policy, warnings := process.ParseStopPolicy(resolved.Tree)
			if len(warnings) != 0 {
				t.Fatalf("ParseStopPolicy warnings = %v", warnings)
			}
			if policy.GracefulTimeout != 120*time.Second || policy.TermTimeout != time.Minute {
				t.Fatalf("embedded MongoDB stop windows = %+v", policy)
			}
		})
	}
}
