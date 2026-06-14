package checks

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

// connCheck probes a server over a connection protocol (mysql, …): it connects,
// authenticates and verifies the server responds. The protocol comes from the
// conn registry, keyed by the check type, so new protocols need no change here.
// probe defaults to proto.Probe and is injectable for tests.
type connCheck struct {
	base
	proto conn.Protocol
	cfg   conn.Config
	probe func(context.Context, conn.Config) (conn.Result, error)
	// onChange alerts when the server's fingerprint (Result.Extra["fingerprint"],
	// e.g. an SSH host key) changes between cycles. onVersionChange alerts when the
	// server's version identity changes (its reported version, or the connection
	// greeting banner for protocols that have no version — smtp/imap/pop/ftp).
	// state holds the previous values; being a pointer, it survives across cycles
	// when the check is built once (a host watch), like the cert check.
	onChange        bool
	onVersionChange bool
	state           *connState
	// expect holds optional response assertions: each compares a field of the
	// probe Result ("version" or a Result.Extra key) against a value with a
	// shared operator. All must hold for the check to pass (additive to the
	// liveness probe). Reuses the expect_json triple shape and compareValue.
	expect []jsonAssertion
	// latencyOp/latencyValue optionally compare the probe's response time in ms
	// (expect_latency), like the http check.
	latencyOp    string
	latencyValue string
	// ifaces optionally pins the probe to one or more egress interfaces
	// (name/IP/MAC); ifaceAll requires every one to succeed (else any).
	ifaces   []string
	ifaceAll bool
}

type connState struct {
	primed          bool
	lastFingerprint string
	lastVersion     string
}

// versionIdentity is the string tracked for on_version_change: the protocol's
// reported version, or the connection greeting banner when it has none (so
// smtp/imap/pop/ftp, which expose only a greeting, still detect version changes).
func versionIdentity(res conn.Result) string {
	if res.Version != "" {
		return res.Version
	}
	return res.Extra["greeting"]
}

func trimConnResult(res conn.Result) conn.Result {
	res.Version = TrimOutput(res.Version)
	if len(res.Extra) == 0 {
		return res
	}
	extra := make(map[string]string, len(res.Extra))
	for k, v := range res.Extra {
		extra[k] = TrimOutput(v)
	}
	res.Extra = extra
	return res
}

func (c connCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	probe := c.probe
	if probe == nil {
		probe = c.proto.Probe
	}
	addr := c.cfg.Socket
	if addr == "" {
		addr = net.JoinHostPort(c.cfg.Host, strconv.Itoa(c.cfg.Port))
	}
	var res conn.Result
	var elapsed time.Duration
	_, perIface, err := tryInterfaces(c.ifaces, c.ifaceAll, func(iface string) error {
		cfg := c.cfg
		cfg.Interface = iface
		t0 := time.Now()
		r, e := probe(ctx, cfg)
		if e == nil {
			res, elapsed = trimConnResult(r), time.Since(t0)
		}
		return e
	})
	if err != nil {
		r := c.result(false, fmt.Sprintf("%s %s: %v", c.proto.Name(), addr, err), start)
		r.Data = ifaceData(perIface)
		return r
	}
	if c.state != nil {
		var problems []string
		extra := map[string]any{}
		if c.onChange {
			fp := res.Extra["fingerprint"]
			if c.state.primed && fp != c.state.lastFingerprint {
				problems = append(problems, fmt.Sprintf("fingerprint changed (%s -> %s)", c.state.lastFingerprint, fp))
				extra["fingerprint_old"] = c.state.lastFingerprint
			}
			c.state.lastFingerprint, extra["fingerprint"] = fp, fp
		}
		if c.onVersionChange {
			v := versionIdentity(res)
			if c.state.primed && v != c.state.lastVersion {
				problems = append(problems, fmt.Sprintf("version changed (%s -> %s)", c.state.lastVersion, v))
				extra["version_old"] = c.state.lastVersion
			}
			c.state.lastVersion, extra["version"] = v, v
		}
		primed := c.state.primed
		c.state.primed = true
		if primed && len(problems) > 0 {
			r := c.result(false, fmt.Sprintf("%s %s: %s", c.proto.Name(), addr, strings.Join(problems, "; ")), start)
			r.Data = map[string]any{"protocol": c.proto.Name(), "host": c.cfg.Host, "port": c.cfg.Port, "latency_ms": elapsed.Milliseconds()}
			for k, v := range extra {
				r.Data[k] = v
			}
			return r
		}
	}
	msg := fmt.Sprintf("%s %s ok", c.proto.Name(), addr)
	if res.Version != "" {
		msg += " (" + res.Version + ")"
	}
	ok := true
	if fail := c.evalExpect(res); fail != "" {
		ok, msg = false, fmt.Sprintf("%s %s: %s", c.proto.Name(), addr, fail)
	}
	if ok && c.latencyOp != "" {
		ms := strconv.FormatInt(elapsed.Milliseconds(), 10)
		pass, lerr := compareValue(ms, c.latencyOp, c.latencyValue)
		switch {
		case lerr != nil:
			ok, msg = false, fmt.Sprintf("%s %s: latency: %v", c.proto.Name(), addr, lerr)
		case !pass:
			ok, msg = false, fmt.Sprintf("%s %s: latency %sms not %s %s", c.proto.Name(), addr, ms, c.latencyOp, c.latencyValue)
		}
	}
	r := c.result(ok, msg, start)
	r.Data = map[string]any{"protocol": c.proto.Name(), "latency_ms": elapsed.Milliseconds()}
	if c.cfg.Socket != "" {
		r.Data["socket"] = c.cfg.Socket
	} else {
		r.Data["host"], r.Data["port"] = c.cfg.Host, c.cfg.Port
	}
	if perIface != nil {
		r.Data["interfaces"] = perIface
	}
	if res.Version != "" {
		r.Data["version"] = res.Version
	}
	for k, v := range res.Extra {
		r.Data[k] = v
	}
	return r
}

// evalExpect checks every configured assertion against the probe result and
// returns the first failure ("" when all hold or none are configured). A field
// is "version" (the Result.Version) or a key of Result.Extra.
func (c connCheck) evalExpect(res conn.Result) string {
	for _, a := range c.expect {
		var got string
		if a.path == "version" {
			got = res.Version
		} else {
			v, ok := res.Extra[a.path]
			if !ok {
				return fmt.Sprintf("field %q not available", a.path)
			}
			got = v
		}
		ok, err := compareValue(got, a.op, a.value)
		if err != nil {
			return fmt.Sprintf("%s: %v", a.path, err)
		}
		if !ok {
			return fmt.Sprintf("%s %q %s %q not satisfied", a.path, got, a.op, a.value)
		}
	}
	return ""
}

// buildConnCheck builds a connection-protocol check for a registered protocol.
// The password arrives already resolved from ${env:...} by the config loader.
func buildConnCheck(b base, proto conn.Protocol, entry map[string]any) (Check, string) {
	user := cfgval.AsString(entry["user"])
	if user == "" && proto.RequiresUser() {
		return nil, proto.Name() + " check requires a user"
	}
	host := cfgval.AsString(entry["host"])
	if host == "" {
		host = "127.0.0.1"
	}
	port := proto.DefaultPort()
	if p, ok := cfgval.Int(entry["port"]); ok {
		port = p
	}
	cfg := conn.Config{
		Host:     host,
		Port:     port,
		Socket:   cfgval.AsString(entry["socket"]),
		User:     user,
		Password: cfgval.AsString(entry["password"]),
		Database: cfgval.AsString(entry["database"]),
		Query:    cfgval.AsString(entry["query"]),
		TLS:      tlsString(entry["tls"]),
		// cfg.Interface is set per-attempt by connCheck.Run from the interface set;
		// it pins the probe's egress (SO_BINDTODEVICE) on multi-homed hosts.
	}
	// dns takes an optional `resolvconf: true`, querying the first nameserver of
	// /etc/resolv.conf instead of a fixed host (with pppd's usepeerdns, the
	// provider's resolver). Scoped here so it never leaks into other protocols.
	if proto.Name() == "dns" {
		if cfgval.Bool(entry["resolvconf"]) {
			if cfgval.AsString(entry["host"]) != "" {
				return nil, "dns check: host and resolvconf are mutually exclusive"
			}
			cfg.Params = map[string]string{"resolvconf": "true"}
		}
	}
	// dhcp takes an optional fixed client MAC (absent -> a random anonymous MAC).
	// Scoped to dhcp so it never leaks into the driver params other protocols pass
	// through cfg.Params. Its egress interface uses the shared cfg.Interface.
	if proto.Name() == "dhcp" {
		if mac := cfgval.AsString(entry["mac"]); mac != "" {
			if _, err := net.ParseMAC(mac); err != nil {
				return nil, fmt.Sprintf("dhcp check: invalid mac %q", mac)
			}
			cfg.Params = map[string]string{"mac": mac}
		}
	}
	// dhclient can optionally validate an active ISC dhclient lease file.
	if proto.Name() == "dhclient" {
		if leaseFile := cfgval.AsString(entry["lease_file"]); leaseFile != "" {
			cfg.Query = leaseFile
		}
	}
	// openvpn defaults to UDP; `transport: tcp` selects TCP (length-prefixed
	// framing). Scoped here so it never leaks into other protocols' params.
	if proto.Name() == "openvpn" {
		if tr := strings.ToLower(cfgval.AsString(entry["transport"])); tr != "" {
			if tr != "udp" && tr != "tcp" {
				return nil, fmt.Sprintf("openvpn check: transport must be udp or tcp, got %q", tr)
			}
			cfg.Params = map[string]string{"transport": tr}
		}
	}
	// mongodb takes an optional auth_source (the credentials database); MongoConnect
	// defaults it to the target database, then admin. Scoped here so it never leaks
	// into other protocols' params.
	if proto.Name() == "mongodb" {
		if as := cfgval.AsString(entry["auth_source"]); as != "" {
			cfg.Params = map[string]string{"auth_source": as}
		}
	}
	// fpm takes an optional `status_path` (the pool's pm.status_path); set, the
	// probe fetches the status page and exposes the pool metrics, carried in
	// cfg.Query. Absent, the probe does a plain /ping liveness check.
	if proto.Name() == "fpm" {
		if sp := cfgval.AsString(entry["status_path"]); sp != "" {
			cfg.Query = sp
		}
	}
	// nut takes an optional `ups` (the UPS to read variables from / LOGIN to),
	// carried in cfg.Query; absent, the probe auto-detects a single configured UPS.
	if proto.Name() == "nut" {
		if u := cfgval.AsString(entry["ups"]); u != "" {
			cfg.Query = u
		}
	}
	// docker takes an optional `container` (name/id whose state/health to read),
	// carried in cfg.Query, and defaults to the local Engine Unix socket.
	if proto.Name() == "docker" {
		if c := cfgval.AsString(entry["container"]); c != "" {
			cfg.Query = c
		}
		if cfg.Socket == "" && cfgval.AsString(entry["host"]) == "" {
			cfg.Socket = "/run/docker.sock"
		}
	}
	// libvirt defaults to the local Unix socket; an explicit host selects TCP. An
	// optional `domain` selects a single VM whose state to read (the connect URI
	// stays in cfg.Query, so the VM name is carried in cfg.Params).
	if proto.Name() == "libvirt" {
		if cfg.Socket == "" && cfgval.AsString(entry["host"]) == "" {
			cfg.Socket = "/run/libvirt/libvirt-sock"
		}
		if d := cfgval.AsString(entry["domain"]); d != "" {
			cfg.Params = map[string]string{"domain": d}
		}
	}
	// acpid is socket-only; default to its well-known event socket.
	if proto.Name() == "acpid" && cfg.Socket == "" {
		cfg.Socket = "/run/acpid.socket"
	}
	// fail2ban is socket-only; default to its well-known control socket.
	if proto.Name() == "fail2ban" && cfg.Socket == "" {
		cfg.Socket = "/run/fail2ban/fail2ban.sock"
	}
	// lvmpolld is socket-only; default to its well-known control socket.
	if proto.Name() == "lvmpolld" && cfg.Socket == "" {
		cfg.Socket = "/run/lvm/lvmpolld.socket"
	}
	// dbus and avahi (probed over D-Bus) resolve to a single D-Bus address
	// (socket path or full address), stored in Socket so the check message shows
	// it instead of host:port.
	if proto.Name() == "dbus" || proto.Name() == "avahi" {
		cfg.Socket = conn.DBusAddress(cfgval.AsString(entry["socket"]), cfgval.AsString(entry["query"]))
	}
	c := connCheck{base: b, proto: proto, cfg: cfg, probe: proto.Probe}
	// Optional response assertions: a mapping of field -> value | {op, value},
	// compared against the probe Result (version / Extra) — works for any protocol.
	expect := parseJSONAssertions(entry["expect"])
	for _, a := range expect {
		if !validCompareOp(a.op) {
			return nil, proto.Name() + " check: expect." + a.path + " op must be one of ==, !=, >, >=, <, <=, =~"
		}
	}
	c.expect = expect
	lop, lval, lwarn := parseExpectLatency(entry)
	if lwarn != "" {
		return nil, proto.Name() + " check: " + lwarn
	}
	c.latencyOp, c.latencyValue = lop, lval
	c.onChange = cfgval.Bool(entry["on_change"])
	c.onVersionChange = cfgval.Bool(entry["on_version_change"])
	if c.onChange || c.onVersionChange {
		c.state = &connState{}
	}
	c.ifaces = parseInterfaces(entry["interface"])
	all, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, proto.Name() + " check: " + iwarn
	}
	c.ifaceAll = all
	return c, ""
}

// tlsString reads a tls field that may be a YAML bool (true/false) or a string
// (e.g. "skip-verify").
func tlsString(v any) string {
	switch t := v.(type) {
	case bool:
		if t {
			return "true"
		}
		return "false"
	case string:
		return t
	default:
		return ""
	}
}
