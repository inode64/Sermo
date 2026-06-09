package config

import "testing"

func TestValidateMySQLCheckValid(t *testing.T) {
	issues := validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: mysql, user: monitor, password: secret, port: 3306, tls: false }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.conn") {
			t.Fatalf("a valid mysql check must produce no issue: %v", issues)
		}
	}
}

func TestValidateMySQLCheckRequiresUser(t *testing.T) {
	mustHave(t, validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: mysql }
`), "user is required")
}

func TestValidatePostgresSSLModeAccepted(t *testing.T) {
	issues := validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: postgres, user: monitor, tls: verify-full }
`)
	for _, is := range issues {
		if hasIssue([]Issue{is}, "checks.conn") {
			t.Fatalf("postgres tls=verify-full must be valid: %v", issues)
		}
	}
}

func TestValidateMySQLCheckBadTLS(t *testing.T) {
	mustHave(t, validateService(t, `
kind: service
name: db
service: { name: x }
checks:
  conn: { type: mariadb, user: u, tls: maybe }
`), "tls")
}
