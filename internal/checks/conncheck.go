package checks

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

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
	// e.g. an SSH host key) changes between cycles. state holds the previous value;
	// being a pointer, it survives across cycles when the check is built once (a
	// host watch), matching the cert check's change detection.
	onChange bool
	state    *connState
}

type connState struct {
	primed          bool
	lastFingerprint string
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
	res, err := probe(ctx, c.cfg)
	if err != nil {
		return c.result(false, fmt.Sprintf("%s %s: %v", c.proto.Name(), addr, err), start)
	}
	if c.onChange && c.state != nil {
		fp := res.Extra["fingerprint"]
		changed := c.state.primed && fp != c.state.lastFingerprint
		old := c.state.lastFingerprint
		c.state.primed, c.state.lastFingerprint = true, fp
		if changed {
			r := c.result(false, fmt.Sprintf("%s %s: fingerprint changed (%s -> %s)", c.proto.Name(), addr, old, fp), start)
			r.Data = map[string]any{"protocol": c.proto.Name(), "host": c.cfg.Host, "port": c.cfg.Port, "fingerprint": fp, "fingerprint_old": old}
			return r
		}
	}
	msg := fmt.Sprintf("%s %s ok", c.proto.Name(), addr)
	if res.Version != "" {
		msg += " (" + res.Version + ")"
	}
	r := c.result(true, msg, start)
	r.Data = map[string]any{"protocol": c.proto.Name()}
	if c.cfg.Socket != "" {
		r.Data["socket"] = c.cfg.Socket
	} else {
		r.Data["host"], r.Data["port"] = c.cfg.Host, c.cfg.Port
	}
	if res.Version != "" {
		r.Data["version"] = res.Version
	}
	for k, v := range res.Extra {
		r.Data[k] = v
	}
	return r
}

// buildConnCheck builds a connection-protocol check for a registered protocol.
// The password arrives already resolved from ${env:...} by the config loader.
func buildConnCheck(b base, proto conn.Protocol, entry map[string]any) (Check, string) {
	user := asString(entry["user"])
	if user == "" && proto.RequiresUser() {
		return nil, proto.Name() + " check requires a user"
	}
	host := asString(entry["host"])
	if host == "" {
		host = "127.0.0.1"
	}
	port := proto.DefaultPort()
	if p, ok := intField(entry["port"]); ok {
		port = p
	}
	cfg := conn.Config{
		Host:     host,
		Port:     port,
		Socket:   asString(entry["socket"]),
		User:     user,
		Password: asString(entry["password"]),
		Database: asString(entry["database"]),
		Query:    asString(entry["query"]),
		TLS:      tlsString(entry["tls"]),
	}
	// dhcp takes two protocol-specific params: the network interface to
	// broadcast on (absent -> unicast to host) and an optional fixed client MAC
	// (absent -> a random anonymous MAC). Scoped to dhcp so they never leak into
	// the driver params other protocols pass through cfg.Params.
	if proto.Name() == "dhcp" {
		params := map[string]string{}
		if iface := asString(entry["interface"]); iface != "" {
			params["interface"] = iface
		}
		if mac := asString(entry["mac"]); mac != "" {
			if _, err := net.ParseMAC(mac); err != nil {
				return nil, fmt.Sprintf("dhcp check: invalid mac %q", mac)
			}
			params["mac"] = mac
		}
		if len(params) > 0 {
			cfg.Params = params
		}
	}
	// libvirt defaults to the local Unix socket; an explicit host selects TCP.
	if proto.Name() == "libvirt" && cfg.Socket == "" && asString(entry["host"]) == "" {
		cfg.Socket = "/var/run/libvirt/libvirt-sock"
	}
	c := connCheck{base: b, proto: proto, cfg: cfg, probe: proto.Probe}
	if asBool(entry["on_change"]) {
		c.onChange = true
		c.state = &connState{}
	}
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
