package checks

import (
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

// buildTCPCheck builds a tcp connectivity check.
func buildTCPCheck(b base, entry map[string]any) (Check, string) {
	port, ok := cfgval.Int(entry[CheckKeyPort])
	if !ok {
		return nil, "tcp check requires a numeric port"
	}
	host := cfgval.AsString(entry[CheckKeyHost])
	if host == "" {
		host = conn.DefaultHost
	}
	all, warning := parseInterfaceMatch(entry)
	if warning != "" {
		return nil, "tcp check: " + warning
	}
	return tcpCheck{base: b, host: host, ifaces: parseInterfaces(entry[CheckKeyInterface]), ifaceAll: all, port: port}, ""
}

// buildPortsCheck builds a multi-port open/closed check.
func buildPortsCheck(b base, entry map[string]any) (Check, string) {
	host := cfgval.AsString(entry[CheckKeyHost])
	if host == "" {
		host = conn.DefaultHost
	}
	ports, err := ParsePortSpec(cfgval.AsString(entry[CheckKeyPorts]))
	if err != nil {
		return nil, "ports check: " + err.Error()
	}
	expect := cfgval.AsString(entry[CheckKeyExpect])
	if expect == "" {
		expect = PortStateOpen
	}
	if expect != PortStateOpen && expect != PortStateClosed && expect != PortExpectAny {
		return nil, "ports check: expect must be " + PortExpectSummary
	}
	match := cfgval.AsString(entry[CheckKeyMatch])
	if match == "" {
		match = PortMatchAll
	}
	if match != PortMatchAll && match != PortMatchAny && match != PortMatchNone {
		return nil, "ports check: match must be " + PortMatchSummary
	}
	connectTimeout := time.Duration(0)
	if raw, present := entry[CheckKeyConnectTimeout]; present {
		connectTimeout = cfgval.Duration(raw)
		if connectTimeout <= 0 {
			return nil, "ports check: connect_timeout must be a valid positive duration"
		}
	}
	allIf, warning := parseInterfaceMatch(entry)
	if warning != "" {
		return nil, "ports check: " + warning
	}
	return &portsCheck{base: b, host: host, ifaces: parseInterfaces(entry[CheckKeyInterface]), ifaceAll: allIf, ports: ports, expect: expect, match: match, onChange: cfgval.Bool(entry[CheckKeyOnChange]), connectTimeout: connectTimeout}, ""
}
