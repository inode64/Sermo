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
		TLS:      tlsString(entry["tls"]),
	}
	return connCheck{base: b, proto: proto, cfg: cfg, probe: proto.Probe}, ""
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
