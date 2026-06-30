package conn

import (
	"context"
	"testing"
	"time"

	g "github.com/gosnmp/gosnmp"
)

func TestBuildSNMPParamsV2c(t *testing.T) {
	// No user, no password -> v2c, community "public" (anonymous).
	p := buildSNMPParams(context.Background(), Config{Host: "dev", Port: 161}, time.Second)
	if p.Version != g.Version2c || p.Community != "public" {
		t.Fatalf("v2c default = %v / %q", p.Version, p.Community)
	}
	// Password without a user -> v2c with that community.
	p = buildSNMPParams(context.Background(), Config{Host: "dev", Password: "secret"}, time.Second)
	if p.Version != g.Version2c || p.Community != "secret" {
		t.Fatalf("v2c community = %v / %q", p.Version, p.Community)
	}
}

func TestBuildSNMPParamsV3(t *testing.T) {
	p := buildSNMPParams(context.Background(), Config{Host: "dev", User: "monitor", Password: "authpass"}, time.Second)
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
	p = buildSNMPParams(context.Background(), Config{Host: "dev", User: "monitor"}, time.Second)
	if p.MsgFlags != g.NoAuthNoPriv {
		t.Fatalf("user without password must be noAuthNoPriv, got %v", p.MsgFlags)
	}
}

func TestSNMPByOIDAndString(t *testing.T) {
	vars := []g.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: g.OctetString, Value: []byte("Linux dev 6.0")},
		{Name: "1.3.6.1.2.1.1.2.0", Type: g.ObjectIdentifier, Value: ".1.3.6.1.4.1.8072.3.2.10"}, // no leading dot
		{Name: ".1.3.6.1.2.1.1.5.0", Type: g.OctetString, Value: []byte("router1")},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: g.TimeTicks, Value: uint32(123456)}, // hundredths of a second
	}
	by := snmpByOID(vars)
	if got := snmpString(by[oidSysDescr]); got != "Linux dev 6.0" {
		t.Fatalf("sysDescr = %q", got)
	}
	// Lookup normalizes the missing leading dot.
	if got := snmpString(by[oidSysObjectID]); got != ".1.3.6.1.4.1.8072.3.2.10" {
		t.Fatalf("sysObjectID = %q", got)
	}
	if got := snmpString(by[oidSysName]); got != "router1" {
		t.Fatalf("sysName = %q", got)
	}
	// A TimeTicks value is not text, so snmpString returns "".
	if got := snmpString(by[oidSysUpTime]); got != "" {
		t.Fatalf("uptime via snmpString = %q, want empty", got)
	}
}
