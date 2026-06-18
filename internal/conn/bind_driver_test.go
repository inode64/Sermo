package conn

import (
	"testing"
	"time"
)

func TestMongoConnectWithInterface(t *testing.T) {
	client, err := MongoConnect(Config{Host: "127.0.0.1", Interface: "eth0"})
	if err != nil {
		t.Fatalf("MongoConnect: %v", err)
	}
	MongoDisconnect(client)
}

func TestPostgresConnectorWithInterface(t *testing.T) {
	if _, err := postgresConnector(Config{User: "u", Interface: "eth0"}); err != nil {
		t.Fatalf("postgresConnector: %v", err)
	}
}

func TestPQDialerInterfaceBinding(t *testing.T) {
	d := pqDialer("eth0")
	if d.Dialer == nil || d.Dialer.Control == nil {
		t.Fatal("pq dialer must use BindDialer when interface is set")
	}
}

func TestMySQLConfigInterfaceBinding(t *testing.T) {
	cfg := buildMySQLConfig(Config{User: "u", Password: "p", Interface: "eth0"})
	if cfg.DialFunc == nil {
		t.Fatal("mysql config must set DialFunc when interface is set")
	}
}

func TestLDAPProbeDialerInterfaceBinding(t *testing.T) {
	d := probeDialer("eth0", time.Second)
	if d.Control == nil {
		t.Fatal("LDAP probe dialer must use BindDialer when interface is set")
	}
}
