package conn

import (
	"testing"
	"time"

	g "github.com/gosnmp/gosnmp"
)

func TestSNMPRegistered(t *testing.T) {
	p, ok := Lookup("snmp")
	if !ok {
		t.Fatal("snmp not registered")
	}
	if p.DefaultPort() != 161 {
		t.Fatalf("default port = %d, want 161", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("snmp must not require a user (v2c community is anonymous-style)")
	}
}

func TestBuildSNMPParamsV2c(t *testing.T) {
	// No user, no password -> v2c, community "public" (anonymous).
	p := buildSNMPParams(Config{Host: "dev", Port: 161}, time.Second)
	if p.Version != g.Version2c || p.Community != "public" {
		t.Fatalf("v2c default = %v / %q", p.Version, p.Community)
	}
	// Password without a user -> v2c with that community.
	p = buildSNMPParams(Config{Host: "dev", Password: "secret"}, time.Second)
	if p.Version != g.Version2c || p.Community != "secret" {
		t.Fatalf("v2c community = %v / %q", p.Version, p.Community)
	}
}

func TestBuildSNMPParamsV3(t *testing.T) {
	p := buildSNMPParams(Config{Host: "dev", User: "monitor", Password: "authpass"}, time.Second)
	if p.Version != g.Version3 || p.SecurityModel != g.UserSecurityModel {
		t.Fatalf("v3 = %v / %v", p.Version, p.SecurityModel)
	}
	if p.MsgFlags != g.AuthNoPriv {
		t.Fatalf("with an auth password the level must be authNoPriv, got %v", p.MsgFlags)
	}
	usm, ok := p.SecurityParameters.(*g.UsmSecurityParameters)
	if !ok {
		t.Fatalf("SecurityParameters type = %T", p.SecurityParameters)
	}
	if usm.UserName != "monitor" || usm.AuthenticationProtocol != g.SHA || usm.AuthenticationPassphrase != "authpass" {
		t.Fatalf("usm = %+v", usm)
	}

	// User without a password -> v3 noAuthNoPriv.
	p = buildSNMPParams(Config{Host: "dev", User: "monitor"}, time.Second)
	if p.MsgFlags != g.NoAuthNoPriv {
		t.Fatalf("user without password must be noAuthNoPriv, got %v", p.MsgFlags)
	}
}

func TestExtractSNMP(t *testing.T) {
	vars := []g.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: g.OctetString, Value: []byte("Linux dev 6.0")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: g.ObjectIdentifier, Value: ".1.3.6.1.4.1.8072.3.2.10"},
	}
	descr, oid := extractSNMP(vars)
	if descr != "Linux dev 6.0" {
		t.Fatalf("sysDescr = %q", descr)
	}
	if oid != ".1.3.6.1.4.1.8072.3.2.10" {
		t.Fatalf("sysObjectID = %q", oid)
	}
}
