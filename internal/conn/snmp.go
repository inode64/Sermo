package conn

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	g "github.com/gosnmp/gosnmp"
)

func init() { Register(snmpProtocol{}) }

// System MIB (SNMPv2-MIB, .1.3.6.1.2.1.1) OIDs queried for the health check.
const (
	oidSysDescr    = ".1.3.6.1.2.1.1.1.0"
	oidSysObjectID = ".1.3.6.1.2.1.1.2.0"
	oidSysUpTime   = ".1.3.6.1.2.1.1.3.0"
	oidSysContact  = ".1.3.6.1.2.1.1.4.0"
	oidSysName     = ".1.3.6.1.2.1.1.5.0"
	oidSysLocation = ".1.3.6.1.2.1.1.6.0"
)

const (
	defaultSNMPPort         = 161
	defaultSNMPProbeTimeout = 5 * time.Second
	defaultSNMPRetries      = 1
	defaultSNMPCommunity    = "public"
	snmpTimeTicksPerSecond  = 100
)

// snmpProtocol probes an SNMP agent using gosnmp. With no user it uses SNMPv2c
// (community from password, default "public" — the anonymous/shared-secret
// model). With a user it uses SNMPv3 USM (a password adds SHA authentication,
// authNoPriv). It reads the system description and object id; the object id is
// exposed as the fingerprint so a watch with `on_change` alerts when the device
// identity changes. SNMPv3 USM is why a library is used rather than a hand-rolled
// ASN.1 implementation.
type snmpProtocol struct{}

func (snmpProtocol) Name() string       { return ProtocolNameSNMP }
func (snmpProtocol) DefaultPort() int   { return defaultSNMPPort }
func (snmpProtocol) RequiresUser() bool { return false }

func (snmpProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	timeout := defaultSNMPProbeTimeout
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d > 0 {
			timeout = d
		}
	}
	params := buildSNMPParams(ctx, cfg, timeout)
	if err := params.Connect(); err != nil {
		return Result{}, err
	}
	defer func() { _ = params.Conn.Close() }()

	pkt, err := params.Get([]string{oidSysDescr, oidSysObjectID, oidSysUpTime, oidSysContact, oidSysName, oidSysLocation})
	if err != nil {
		return Result{}, err
	}
	by := snmpByOID(pkt.Variables)
	sysDescr := snmpString(by[oidSysDescr])
	sysObjectID := snmpString(by[oidSysObjectID])
	if sysDescr == "" && sysObjectID == "" {
		return Result{}, fmt.Errorf("snmp: no system MIB values returned (wrong community/credentials?)")
	}
	extra := map[string]string{
		ExtraKeyFingerprint: sysObjectID, // device identity; watched by on_change
		"sys_object_id":     sysObjectID,
		"snmp_version":      snmpVersionName(cfg),
	}
	// Identification fields the agent exposes alongside the object id, each
	// assertable via expect: (e.g. sys_name == host).
	putIfSet(extra, extraSysName, snmpString(by[oidSysName]))
	putIfSet(extra, extraSysContact, snmpString(by[oidSysContact]))
	putIfSet(extra, extraSysLocation, snmpString(by[oidSysLocation]))
	if up, ok := by[oidSysUpTime]; ok {
		switch up.Type {
		case g.TimeTicks, g.Integer, g.Counter32, g.Gauge32, g.Counter64:
			extra[extraSysUptimeSeconds] = strconv.FormatInt(g.ToBigInt(up.Value).Int64()/snmpTimeTicksPerSecond, numericBaseDecimal)
		}
	}
	return Result{Version: sysDescr, Extra: extra}, nil
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
func buildSNMPParams(ctx context.Context, cfg Config, timeout time.Duration) *g.GoSNMP {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultSNMPPort
	}
	p := &g.GoSNMP{
		Target:    host,
		Port:      uint16(port),
		Transport: networkUDP,
		Context:   ctx,
		Timeout:   timeout,
		Retries:   defaultSNMPRetries,
		MaxOids:   g.MaxOids,
	}
	if cfg.Interface != "" {
		p.Control = BindDialer(cfg.Interface).Control
	}
	if cfg.User == "" {
		p.Version = g.Version2c
		p.Community = cfg.Password
		if p.Community == "" {
			p.Community = defaultSNMPCommunity
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

// snmpByOID indexes the returned varbinds by their OID, normalized to a leading
// dot so lookups with the OID constants match regardless of how the agent
// formats the name.
func snmpByOID(vars []g.SnmpPDU) map[string]g.SnmpPDU {
	out := make(map[string]g.SnmpPDU, len(vars))
	for _, v := range vars {
		out["."+strings.TrimPrefix(v.Name, ".")] = v
	}
	return out
}

// snmpString renders a varbind's value as text: OctetString ([]byte) and
// ObjectIdentifier (string) cover the system-group fields; anything else (or an
// absent/NoSuchObject varbind) yields "".
func snmpString(v g.SnmpPDU) string {
	switch val := v.Value.(type) {
	case []byte:
		return string(val)
	case string:
		return val
	default:
		return ""
	}
}

// putIfSet adds k=v to m only when v is non-empty, so absent SNMP fields don't
// surface as empty Extra keys.
func putIfSet(m map[string]string, k, v string) {
	if v != "" {
		m[k] = v
	}
}
