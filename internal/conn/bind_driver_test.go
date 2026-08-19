package conn

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// testExternalModuleInterfaceBinding consolidates the per-module
// interface-binding checks used by TestExternalModuleTransportContract. Add a
// subtest whenever a module gains a dialer/client adapter.
func testExternalModuleInterfaceBinding(t *testing.T) {
	cases := []struct {
		name  string
		check func(t *testing.T)
	}{
		{"mongo", func(t *testing.T) {
			t.Helper()
			client, err := MongoConnect(context.Background(), Config{Host: "127.0.0.1", Interface: "eth0"})
			if err != nil {
				t.Fatalf("MongoConnect: %v", err)
			}
			MongoDisconnect(context.Background(), client)
		}},
		{"postgres-config", func(t *testing.T) {
			t.Helper()
			cfg, err := postgresConfig(context.Background(), Config{User: "u", Interface: "eth0"})
			if err != nil {
				t.Fatalf("postgresConfig: %v", err)
			}
			if cfg.DialFunc == nil {
				t.Fatal("pgx config must use BindDialer when interface is set")
			}
		}},
		{"mysql-config", func(t *testing.T) {
			t.Helper()
			cfg := buildMySQLConfig(Config{User: "u", Password: "p", Interface: "eth0"})
			if cfg.DialFunc == nil {
				t.Fatal("mysql config must set DialFunc when interface is set")
			}
		}},
		{"ldap-probe-dialer", func(t *testing.T) {
			t.Helper()
			d := newProbeTarget(Config{Interface: "eth0"}, defaultLDAPPort).dialerWithTimeout(time.Second)
			if d.Control == nil {
				t.Fatal("LDAP probe dialer must use BindDialer when interface is set")
			}
		}},
		{"libvirt-remote-dialer", func(t *testing.T) {
			t.Helper()
			d := libvirtRemoteNetDialer(newProbeTarget(Config{Interface: "eth0"}, defaultPortLibvirt), time.Second)
			if d.Control == nil {
				t.Fatal("libvirt remote dialer must use BindDialer when interface is set")
			}
		}},
		{"http-probe-client", func(t *testing.T) {
			t.Helper()
			client := httpProbeClient("eth0", nil)
			tr, ok := client.Transport.(*http.Transport)
			if !ok || tr.DialContext == nil {
				t.Fatalf("HTTP probe client transport = %#v, want bound DialContext", client.Transport)
			}
		}},
		{"http-probe-base", func(t *testing.T) {
			t.Helper()
			client, base := httpProbeBase(context.Background(), Config{Host: "probe.example", Port: 8443, TLS: tlsSkipVerify, Interface: "eth0"}, 8080)
			if base != "https://probe.example:8443" {
				t.Fatalf("base = %q", base)
			}
			tr, ok := client.Transport.(*http.Transport)
			if !ok || tr.DialContext == nil || tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
				t.Fatalf("HTTP probe base transport = %#v, want bound skip-verify transport", client.Transport)
			}
		}},
		{"http-probe-base-explicit-TLS", func(t *testing.T) {
			t.Helper()
			client, base := httpProbeBaseWithTLSMode(context.Background(), Config{Host: "probe.example", Interface: "eth0"}, 8080, tlsSkipVerify)
			if base != "https://probe.example:8080" {
				t.Fatalf("base = %q", base)
			}
			tr, ok := client.Transport.(*http.Transport)
			if !ok || tr.DialContext == nil || tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
				t.Fatalf("explicit TLS HTTP probe transport = %#v, want bound skip-verify transport", client.Transport)
			}
		}},
		{"snmp-params", func(t *testing.T) {
			t.Helper()
			params := buildSNMPParams(context.Background(), Config{Host: "dev", Interface: "eth0"}, time.Second)
			if params.Control == nil {
				t.Fatal("SNMP params must use BindDialer control hook when interface is set")
			}
			if params.Context == nil {
				t.Fatal("SNMP params must carry the probe context")
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, c.check)
	}
}
