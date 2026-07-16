package conn

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// TestInterfaceBindingApplied consolidates the former per-driver interface-binding
// tests: each case sets Interface and asserts the corresponding driver/dialer/client
// wires up the BindDialer control hook (or equivalent). Add a subtest when a new
// driver gains interface binding.
func TestInterfaceBindingApplied(t *testing.T) {
	cases := []struct {
		name  string
		check func(t *testing.T)
	}{
		{"mongo", func(t *testing.T) {
			t.Helper()
			client, err := MongoConnect(Config{Host: "127.0.0.1", Interface: "eth0"})
			if err != nil {
				t.Fatalf("MongoConnect: %v", err)
			}
			MongoDisconnect(context.Background(), client)
		}},
		{"postgres-connector", func(t *testing.T) {
			t.Helper()
			if _, err := postgresConnector(Config{User: "u", Interface: "eth0"}); err != nil {
				t.Fatalf("postgresConnector: %v", err)
			}
		}},
		{"pq-dialer", func(t *testing.T) {
			t.Helper()
			d := pqDialer("eth0")
			if d.Dialer == nil || d.Dialer.Control == nil {
				t.Fatal("pq dialer must use BindDialer when interface is set")
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
			d := probeDialer("eth0", time.Second)
			if d.Control == nil {
				t.Fatal("LDAP probe dialer must use BindDialer when interface is set")
			}
		}},
		{"libvirt-remote-dialer", func(t *testing.T) {
			t.Helper()
			d := libvirtRemoteNetDialer("eth0", time.Second)
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
			client, base := httpProbeBase(Config{Host: "probe.example", Port: 8443, TLS: tlsSkipVerify, Interface: "eth0"}, 8080)
			if base != "https://probe.example:8443" {
				t.Fatalf("base = %q", base)
			}
			tr, ok := client.Transport.(*http.Transport)
			if !ok || tr.DialContext == nil || tr.TLSClientConfig == nil || !tr.TLSClientConfig.InsecureSkipVerify {
				t.Fatalf("HTTP probe base transport = %#v, want bound skip-verify transport", client.Transport)
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
