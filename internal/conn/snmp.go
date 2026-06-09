package conn

import (
	"context"
	"fmt"
	"strings"
	"time"

	g "github.com/gosnmp/gosnmp"
)

func init() { Register(snmpProtocol{}) }

// System MIB OIDs queried for the health check.
const (
	oidSysDescr    = ".1.3.6.1.2.1.1.1.0"
	oidSysObjectID = ".1.3.6.1.2.1.1.2.0"
)

// snmpProtocol probes an SNMP agent using gosnmp. With no user it uses SNMPv2c
// (community from password, default "public" — the anonymous/shared-secret
// model). With a user it uses SNMPv3 USM (a password adds SHA authentication,
// authNoPriv). It reads the system description and object id; the object id is
// exposed as the fingerprint so a watch with `on_change` alerts when the device
// identity changes. SNMPv3 USM is why a library is used rather than a hand-rolled
// ASN.1 implementation.
type snmpProtocol struct{}

func (snmpProtocol) Name() string       { return "snmp" }
func (snmpProtocol) DefaultPort() int   { return 161 }
func (snmpProtocol) RequiresUser() bool { return false }

func (snmpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	timeout := 5 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			timeout = d
		}
	}
	params := buildSNMPParams(cfg, timeout)
	if err := params.Connect(); err != nil {
		return Result{}, err
	}
	defer func() { _ = params.Conn.Close() }()

	pkt, err := params.Get([]string{oidSysDescr, oidSysObjectID})
	if err != nil {
		return Result{}, err
	}
	sysDescr, sysObjectID := extractSNMP(pkt.Variables)
	if sysDescr == "" && sysObjectID == "" {
		return Result{}, fmt.Errorf("snmp: no system MIB values returned (wrong community/credentials?)")
	}
	return Result{
		Version: sysDescr,
		Extra: map[string]string{
			"fingerprint":   sysObjectID, // device identity; watched by on_change
			"sys_object_id": sysObjectID,
			"snmp_version":  snmpVersionName(cfg),
		},
	}, nil
}

func snmpVersionName(cfg Config) string {
	if cfg.User != "" {
		return "3"
	}
	return "2c"
}

// buildSNMPParams maps the connection config to a gosnmp client: v2c (community)
// when no user is set, otherwise v3 USM (authNoPriv with SHA when a password is
// present, else noAuthNoPriv).
func buildSNMPParams(cfg Config, timeout time.Duration) *g.GoSNMP {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 161
	}
	p := &g.GoSNMP{
		Target:    host,
		Port:      uint16(port),
		Transport: "udp",
		Timeout:   timeout,
		Retries:   1,
		MaxOids:   g.MaxOids,
	}
	if cfg.User == "" {
		p.Version = g.Version2c
		p.Community = cfg.Password
		if p.Community == "" {
			p.Community = "public"
		}
		return p
	}
	p.Version = g.Version3
	p.SecurityModel = g.UserSecurityModel
	usm := &g.UsmSecurityParameters{UserName: cfg.User}
	if cfg.Password != "" {
		p.MsgFlags = g.AuthNoPriv
		usm.AuthenticationProtocol = g.SHA
		usm.AuthenticationPassphrase = cfg.Password
	} else {
		p.MsgFlags = g.NoAuthNoPriv
	}
	p.SecurityParameters = usm
	return p
}

// extractSNMP pulls sysDescr and sysObjectID out of the returned varbinds.
func extractSNMP(vars []g.SnmpPDU) (sysDescr, sysObjectID string) {
	for _, v := range vars {
		switch strings.TrimPrefix(v.Name, ".") {
		case strings.TrimPrefix(oidSysDescr, "."):
			switch val := v.Value.(type) {
			case []byte:
				sysDescr = string(val)
			case string:
				sysDescr = val
			}
		case strings.TrimPrefix(oidSysObjectID, "."):
			if s, ok := v.Value.(string); ok {
				sysObjectID = s
			}
		}
	}
	return sysDescr, sysObjectID
}
