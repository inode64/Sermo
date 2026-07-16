package checks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/netutil"
	"sermo/internal/output"
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
	// onChange alerts when the server's fingerprint (Result.Extra[conn.ExtraKeyFingerprint],
	// e.g. an SSH host key) changes between cycles. onVersionChange alerts when the
	// server's version identity changes (its reported version, or the connection
	// greeting banner for protocols that have no version — smtp/imap/pop/ftp).
	// state holds the previous values; being a pointer, it survives across cycles
	// while the check instance is reused by a service worker or host watch. A
	// config reload/worker rebuild creates a fresh baseline, like the cert check.
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
	return res.Extra[conn.ExtraKeyGreeting]
}

func trimConnResult(res conn.Result) conn.Result {
	res.Version = output.Trim(res.Version)
	if len(res.Extra) == 0 {
		return res
	}
	extra := make(map[string]string, len(res.Extra))
	for k, v := range res.Extra {
		extra[k] = output.Trim(v)
	}
	res.Extra = extra
	return res
}

func (c connCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	addr := c.address()
	res, elapsed, perIface, err := c.probeResult(ctx)
	if err != nil {
		r := c.result(false, fmt.Sprintf("%s %s: %v", c.proto.Name(), addr, err), start)
		r.Data = ifaceData(perIface)
		return r
	}
	if problems, extra, changed := c.changed(res); changed {
		r := c.result(false, fmt.Sprintf("%s %s: %s", c.proto.Name(), addr, strings.Join(problems, "; ")), start)
		r.Data = c.changeData(elapsed)
		maps.Copy(r.Data, extra)
		return r
	}
	ok, msg := c.evaluateResponse(res, elapsed, addr)
	r := c.result(ok, msg, start)
	r.Data = c.resultData(elapsed, perIface, res)
	return r
}

func (c connCheck) address() string {
	if c.cfg.Socket != "" {
		return c.cfg.Socket
	}
	return netutil.JoinHostPort(c.cfg.Host, c.cfg.Port)
}

func (c connCheck) probeResult(ctx context.Context) (conn.Result, time.Duration, map[string]any, error) {
	probe := c.probe
	if probe == nil {
		probe = c.proto.Probe
	}
	var res conn.Result
	var elapsed time.Duration
	_, perIface, err := tryInterfaces(c.ifaces, c.ifaceAll, func(iface string) error {
		cfg := c.cfg
		cfg.Interface = iface
		t0 := time.Now()
		r, e := probe(ctx, cfg)
		if e == nil {
			took := time.Since(t0)
			// any-match returns on the first success, so there is only one. all-match
			// runs every interface; report the worst (slowest) path, mirroring the
			// icmp check's "report the worst path" so latency reflects the bottleneck.
			if !c.ifaceAll || took >= elapsed {
				res, elapsed = trimConnResult(r), took
			}
		}
		return e
	})
	return res, elapsed, perIface, err
}

func (c connCheck) changed(res conn.Result) (problems []string, extra map[string]any, changed bool) {
	if c.state == nil {
		return nil, nil, false
	}
	const connChangeExtraInitialCapacity = 2

	extra = make(map[string]any, connChangeExtraInitialCapacity)
	if c.onChange {
		fingerprint := res.Extra[conn.ExtraKeyFingerprint]
		if c.state.primed && fingerprint != c.state.lastFingerprint {
			problems = append(problems, fmt.Sprintf("fingerprint changed (%s -> %s)", c.state.lastFingerprint, fingerprint))
			extra[DataKeyFingerprintOld] = c.state.lastFingerprint
		}
		c.state.lastFingerprint, extra[DataKeyFingerprint] = fingerprint, fingerprint
	}
	if c.onVersionChange {
		version := versionIdentity(res)
		if c.state.primed && version != c.state.lastVersion {
			problems = append(problems, fmt.Sprintf("version changed (%s -> %s)", c.state.lastVersion, version))
			extra[DataKeyVersionOld] = c.state.lastVersion
		}
		c.state.lastVersion, extra[DataKeyVersion] = version, version
	}
	primed := c.state.primed
	c.state.primed = true
	return problems, extra, primed && len(problems) > 0
}

func (c connCheck) evaluateResponse(res conn.Result, elapsed time.Duration, addr string) (bool, string) {
	msg := fmt.Sprintf("%s %s ok", c.proto.Name(), addr)
	if res.Version != "" {
		msg += " (" + res.Version + ")"
	}
	ok := true
	if fail := c.evalExpect(res); fail != "" {
		ok, msg = false, fmt.Sprintf("%s %s: %s", c.proto.Name(), addr, fail)
	}
	if ok && c.latencyOp != "" {
		ms := strconv.FormatInt(elapsed.Milliseconds(), numericBaseDecimal)
		pass, lerr := compareValue(ms, c.latencyOp, c.latencyValue)
		switch {
		case lerr != nil:
			ok, msg = false, fmt.Sprintf("%s %s: latency: %v", c.proto.Name(), addr, lerr)
		case !pass:
			ok, msg = false, fmt.Sprintf("%s %s: latency %sms not %s %s", c.proto.Name(), addr, ms, c.latencyOp, c.latencyValue)
		}
	}
	return ok, msg
}

func (c connCheck) resultData(elapsed time.Duration, perIface map[string]any, res conn.Result) map[string]any {
	data := map[string]any{DataKeyProtocol: c.proto.Name(), DataKeyLatencyMS: elapsed.Milliseconds()}
	if c.cfg.Socket != "" {
		data[DataKeySocket] = c.cfg.Socket
	} else {
		data[DataKeyHost], data[DataKeyPort] = c.cfg.Host, c.cfg.Port
	}
	if perIface != nil {
		data[DataKeyInterfaces] = perIface
	}
	if res.Version != "" {
		data[DataKeyVersion] = res.Version
	}
	for k, v := range res.Extra {
		data[k] = v
	}
	return data
}

func (c connCheck) changeData(elapsed time.Duration) map[string]any {
	return map[string]any{
		DataKeyProtocol:  c.proto.Name(),
		DataKeyHost:      c.cfg.Host,
		DataKeyPort:      c.cfg.Port,
		DataKeyLatencyMS: elapsed.Milliseconds(),
	}
}

// evalExpect checks every configured assertion against the probe result and
// returns the first failure ("" when all hold or none are configured). A field
// is "version" (the Result.Version) or a key of Result.Extra.
func (c connCheck) evalExpect(res conn.Result) string {
	for _, a := range c.expect {
		var got string
		if a.path == DataKeyVersion {
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
	protoName := proto.Name()
	user := cfgval.AsString(entry[CheckKeyUser])
	if user == "" && proto.RequiresUser() {
		return nil, protoName + " check requires a user"
	}
	cfg := databaseConnectionConfig(entry)
	cfg.Port = connectionPort(entry, proto.DefaultPort())
	cfg.Socket = cfgval.AsString(entry[CheckKeySocket])
	cfg.User = user
	cfg.Query = cfgval.AsString(entry[CheckKeyQuery])
	// cfg.Interface is set per-attempt by connCheck.Run from the interface set;
	// it pins the probe's egress (SO_BINDTODEVICE) on multi-homed hosts.
	if err := configureConnProtocol(&cfg, protoName, entry); err != nil {
		return nil, err.Error()
	}
	c := connCheck{base: b, proto: proto, cfg: cfg, probe: proto.Probe}
	// Optional response assertions: a mapping of field -> value | {op, value},
	// compared against the probe Result (version / Extra) — works for any protocol.
	expect, ewarn := parseAssertionMap(entry[CheckKeyExpect], CheckKeyExpect)
	if ewarn != "" {
		return nil, protoName + " check: " + ewarn
	}
	c.expect = expect
	lop, lval, lwarn := parseExpectLatency(entry)
	if lwarn != "" {
		return nil, protoName + " check: " + lwarn
	}
	c.latencyOp, c.latencyValue = lop, lval
	c.onChange = cfgval.Bool(entry[CheckKeyOnChange])
	c.onVersionChange = cfgval.Bool(entry[CheckKeyOnVersionChange])
	if c.onChange || c.onVersionChange {
		c.state = &connState{}
	}
	c.ifaces = parseInterfaces(entry[CheckKeyInterface])
	all, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, protoName + " check: " + iwarn
	}
	c.ifaceAll = all
	return c, ""
}

func baseConnectionConfig(entry map[string]any) conn.Config {
	cfg := conn.Config{
		Host:     cfgval.AsString(entry[CheckKeyHost]),
		User:     cfgval.AsString(entry[CheckKeyUser]),
		Password: cfgval.AsString(entry[CheckKeyPassword]),
		TLS:      tlsString(entry[CheckKeyTLS]),
	}
	if cfg.Host == "" {
		cfg.Host = conn.DefaultHost
	}
	return cfg
}

func databaseConnectionConfig(entry map[string]any) conn.Config {
	cfg := baseConnectionConfig(entry)
	cfg.Database = cfgval.AsString(entry[CheckKeyDatabase])
	return cfg
}

func connectionPort(entry map[string]any, defaultPort int) int {
	if port, ok := cfgval.Int(entry[CheckKeyPort]); ok {
		return port
	}
	return defaultPort
}

func configureConnProtocol(cfg *conn.Config, protoName string, entry map[string]any) error {
	if socket, ok := defaultConnSockets[protoName]; ok {
		if cfg.Socket == "" {
			cfg.Socket = socket
		}
		return nil
	}
	switch protoName {
	case conn.ProtocolNameDNS:
		return configureDNS(cfg, entry)
	case conn.ProtocolNameDHCP:
		return configureDHCP(cfg, entry)
	case conn.ProtocolNameDHClient:
		setConnQuery(cfg, entry, CheckKeyLeaseFile)
	case conn.ProtocolNameOpenVPN:
		return configureOpenVPN(cfg, entry)
	case conn.ProtocolNameMongoDB:
		setConnParam(cfg, conn.ParamKeyAuthSource, cfgval.AsString(entry[CheckKeyAuthSource]))
	case conn.ProtocolNameFPM:
		setConnQuery(cfg, entry, CheckKeyStatusPath)
	case conn.ProtocolNameNUT:
		setConnQuery(cfg, entry, CheckKeyUPS)
	case conn.ProtocolNameDocker:
		setConnQuery(cfg, entry, CheckKeyContainer)
		setLocalConnSocket(cfg, entry, conn.DefaultDockerSocket)
	case conn.ProtocolNameLibvirt:
		setLocalConnSocket(cfg, entry, conn.DefaultLibvirtSocket)
		setConnParam(cfg, conn.ParamKeyDomain, cfgval.AsString(entry[CheckKeyDomain]))
	case conn.ProtocolNameDBus, conn.ProtocolNameAvahi:
		cfg.Socket = conn.DBusAddress(cfgval.AsString(entry[CheckKeySocket]), cfgval.AsString(entry[CheckKeyQuery]))
	}
	return nil
}

var defaultConnSockets = map[string]string{
	conn.ProtocolNameACPID:    conn.DefaultACPIDSocket,
	conn.ProtocolNameFail2ban: conn.DefaultFail2banSocket,
	conn.ProtocolNameLVMPolld: conn.DefaultLVMPolldSocket,
}

func configureDNS(cfg *conn.Config, entry map[string]any) error {
	if !cfgval.Bool(entry[CheckKeyResolvconf]) {
		return nil
	}
	if cfgval.AsString(entry[CheckKeyHost]) != "" {
		return errors.New("dns check: host and resolvconf are mutually exclusive")
	}
	setConnParam(cfg, conn.ParamKeyResolvconf, conn.ParamValueTrue)
	return nil
}

func configureDHCP(cfg *conn.Config, entry map[string]any) error {
	mac := cfgval.AsString(entry[CheckKeyMAC])
	if mac == "" {
		return nil
	}
	if _, err := net.ParseMAC(mac); err != nil {
		return fmt.Errorf("dhcp check: invalid mac %q", mac)
	}
	setConnParam(cfg, conn.ParamKeyMAC, mac)
	return nil
}

func configureOpenVPN(cfg *conn.Config, entry map[string]any) error {
	transport := strings.ToLower(cfgval.AsString(entry[CheckKeyTransport]))
	if transport == "" {
		return nil
	}
	if transport != conn.TransportUDP && transport != conn.TransportTCP {
		return fmt.Errorf("openvpn check: transport must be %s, got %q", conn.TransportSummary, transport)
	}
	setConnParam(cfg, conn.ParamKeyTransport, transport)
	return nil
}

func setConnQuery(cfg *conn.Config, entry map[string]any, key string) {
	if value := cfgval.AsString(entry[key]); value != "" {
		cfg.Query = value
	}
}

func setConnParam(cfg *conn.Config, key, value string) {
	if value != "" {
		cfg.Params = map[string]string{key: value}
	}
}

func setLocalConnSocket(cfg *conn.Config, entry map[string]any, socket string) {
	if cfg.Socket == "" && cfgval.AsString(entry[CheckKeyHost]) == "" {
		cfg.Socket = socket
	}
}

// tlsString reads a tls field that may be a YAML bool (true/false) or a string
// (e.g. "skip-verify").
func tlsString(v any) string {
	switch t := v.(type) {
	case bool:
		return strconv.FormatBool(t)
	case string:
		return t
	default:
		return ""
	}
}
