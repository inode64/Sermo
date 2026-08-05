package config

import "testing"

func TestValidateTCPConnectionsCheck(t *testing.T) {
	issues := validateService(t, `
name: ftp
service: proftpd
checks:
  clients:
    type: tcp_connections
    port: "21"
    count: { op: ">", value: "5" }
`)
	mustNotHave(t, issues, "checks.clients")
}

func TestValidateTCPConnectionsCheckErrors(t *testing.T) {
	issues := validateService(t, `
name: ftp
service: proftpd
checks:
  missing-port:
    type: tcp_connections
    count: { op: ">", value: "5" }
  invalid-count:
    type: tcp_connections
    port: 21
    count: 5
`)
	mustHave(t, issues, "checks.missing-port.port is required")
	mustHave(t, issues, "checks.invalid-count.count must be a mapping")
}
