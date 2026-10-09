package checks

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/netutil"
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
	// liveness probe). Reuses the expect_json assertion shape and valueMatcher.
	expect []jsonAssertion
	// expectGrades are the kept `levels:` tiers: each restates some expect
	// fields with a looser bound and raises a failure that breaches it too.
	expectGrades grades[[]jsonAssertion]
	// latencyAssertion optionally compares the probe's response time in ms
	// (expect_latency), like the http check.
	latencyAssertion valueMatcher
	// increases optionally bounds how far a numeric probe field may rise within
	// window (max_increase + within) — a key count, a queue depth, a connection
	// total. Evaluated after expect, so both a level and a growth bound can fail
	// the check. clock is injectable for tests.
	increases []connIncrease
	window    time.Duration
	clock     func() time.Time
	// ifaces optionally pins the probe to one or more egress interfaces
	// (name/IP/MAC); ifaceAll requires every one to succeed (else any).
	ifaces   []string
	ifaceAll bool
}

// connIncrease is one max_increase bound: field may rise by at most limit within
// the check's window. state holds the sliding samples; like connState it is a
// pointer so it survives across cycles and re-baselines on a rebuild.
type connIncrease struct {
	field string
	limit float64
	state *counterWindow
}

// connGrowth is one field's reading for a cycle: its current value and the rise
// since the oldest sample still inside the window.
type connGrowth struct {
	field   string
	limit   float64
	current int
	growth  int
	span    time.Duration
	missing bool
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
	res.Version = strings.TrimSpace(res.Version)
	res.Failure = strings.TrimSpace(res.Failure)
	if len(res.Extra) == 0 {
		return res
	}
	extra := make(map[string]string, len(res.Extra))
	for k, v := range res.Extra {
		extra[k] = strings.TrimSpace(v)
	}
	res.Extra = extra
	return res
}

func (c connCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	addr := c.address()
	res, elapsed, perIface, err := c.probeResult(ctx)
	if err != nil {
		r := c.base.unavailableResult(fmt.Sprintf("%s %s: %v", c.proto.Name(), addr, err), start)
		r.Data = c.resultData(0, perIface, conn.Result{})
		delete(r.Data, DataKeyLatencyMS)
		return r
	}
	if problems, extra, changed := c.changed(res); changed {
		r := c.result(false, fmt.Sprintf("%s %s: %s", c.proto.Name(), addr, strings.Join(problems, "; ")), start)
		r.Data = c.resultData(elapsed, perIface, res)
		maps.Copy(r.Data, extra)
		return r
	}
	ok, msg, unavailable := c.evaluateResponse(res, elapsed, addr)
	// Sample every cycle, whatever expect decided: a window that skipped the
	// cycles where another assertion failed would measure growth across a gap.
	growths := c.sampleIncreases(res)
	if ok {
		if fail, missing := increaseFailure(growths); fail != "" {
			ok, unavailable = false, missing
			msg = fmt.Sprintf("%s %s: %s", c.proto.Name(), addr, fail)
		}
	}
	r := c.result(ok, msg, start)
	r.Unavailable = unavailable
	r = gradeExpectResult(r, c.expectGrades, res)
	r.Data = c.resultData(elapsed, perIface, res)
	if r.Observation() == ObservationHealthy {
		r.Data[DataKeySummary] = "Probe succeeded"
	}
	for _, g := range growths {
		if !g.missing {
			r.Data[g.field+DataKeyIncreaseSuffix] = g.growth
		}
	}
	if len(growths) > 0 {
		r.Data[DataKeyWindow] = c.window.String()
	}
	return r
}

// sampleIncreases advances every max_increase window with this cycle's value. A
// field the probe did not return, or one that is not a number, is reported as
// missing and leaves its window untouched.
func (c connCheck) sampleIncreases(res conn.Result) []connGrowth {
	if len(c.increases) == 0 {
		return nil
	}
	now := windowClock(c.clock)()
	out := make([]connGrowth, 0, len(c.increases))
	for _, inc := range c.increases {
		g := connGrowth{field: inc.field, limit: inc.limit}
		value, ok := cfgval.Float(res.Extra[inc.field])
		if !ok {
			g.missing = true
			out = append(out, g)
			continue
		}
		g.current = int(math.Round(value))
		rise, span := inc.state.advance(now, g.current, c.window)
		// A value can legitimately fall — keys expire, a queue drains — so only a
		// rise is growth.
		g.growth, g.span = max(rise, 0), span
		out = append(out, g)
	}
	return out
}

// increaseFailure returns the first max_increase bound that does not hold ("" when
// all do), plus whether it failed for lack of a usable value.
func increaseFailure(growths []connGrowth) (string, bool) {
	for _, g := range growths {
		if g.missing {
			return fmt.Sprintf("field %q not available as a number for %s", g.field, CheckKeyMaxIncrease), true
		}
		if float64(g.growth) > g.limit {
			return fmt.Sprintf("%s grew by %d in %s (%s %s): %d now", g.field, g.growth,
				g.span.Round(time.Second), CheckKeyMaxIncrease, formatThreshold(g.limit), g.current), false
		}
	}
	return "", false
}

func (c connCheck) address() string {
	return targetAddress(c.cfg.Socket, c.cfg.Host, c.cfg.Port)
}

// targetAddress renders a probe target for messages and result data: a Unix
// socket names itself, otherwise it is host:port. Shared with the clock check so
// both report a target the same way.
func targetAddress(socket, host string, port int) string {
	if socket != "" {
		return socket
	}
	return netutil.JoinHostPort(host, port)
}

func (c connCheck) probeResult(ctx context.Context) (conn.Result, time.Duration, map[string]any, error) {
	probe := c.probe
	if probe == nil {
		probe = c.proto.Probe
	}
	var res conn.Result
	var elapsed time.Duration
	var hasProtocolFailure bool
	_, perIface, err := tryInterfaces(c.ifaces, c.ifaceAll, func(iface string) error {
		cfg := c.cfg
		cfg.Interface = iface
		t0 := time.Now()
		r, e := probe(ctx, cfg)
		if e != nil {
			return e
		}
		took := time.Since(t0)
		r = trimConnResult(r)
		if r.Failure != "" {
			// A protocol verdict is a failed interface match, but it is not a
			// transport error: retain its structured evidence so Run can report a
			// normal negative result instead of marking the observation unavailable.
			res, elapsed, hasProtocolFailure = r, took, true
			return errors.New(r.Failure)
		}
		// any-match returns on the first success, so there is only one. all-match
		// runs every interface; report the worst (slowest) path, mirroring the
		// icmp check's "report the worst path" so latency reflects the bottleneck.
		if !c.ifaceAll || took >= elapsed {
			res, elapsed = r, took
		}
		return nil
	})
	if err != nil && hasProtocolFailure {
		// With any-match, an authoritative negative verdict is more informative
		// than a later transport error when no interface succeeds. Returning nil
		// here lets evaluateResponse preserve Failure and Extra as that verdict.
		err = nil
	}
	return res, elapsed, perIface, err
}

func (c connCheck) changed(res conn.Result) (problems []string, extra map[string]any, changed bool) {
	if !c.onChange && !c.onVersionChange {
		return nil, nil, false
	}
	const connChangeExtraInitialCapacity = 2

	extra = make(map[string]any, connChangeExtraInitialCapacity)
	if c.onChange {
		// An observation that carries no identity cannot be compared as one, and
		// must not overwrite the identity already on record. A D-Bus name that is
		// activatable but not currently activated answers without an owner, so
		// treating "" as an identity would report a change every time such a
		// service started or stopped on demand — and again on the first real
		// reading, against a baseline that was never observed.
		if fingerprint := res.Extra[conn.ExtraKeyFingerprint]; fingerprint != "" {
			if c.state.primed && c.state.lastFingerprint != "" && fingerprint != c.state.lastFingerprint {
				problems = append(problems, fmt.Sprintf("fingerprint changed (%s -> %s)", c.state.lastFingerprint, fingerprint))
				extra[DataKeyFingerprintOld] = c.state.lastFingerprint
			}
			c.state.lastFingerprint, extra[DataKeyFingerprint] = fingerprint, fingerprint
		}
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

func (c connCheck) evaluateResponse(res conn.Result, elapsed time.Duration, addr string) (bool, string, bool) {
	msg := fmt.Sprintf("%s %s ok", c.proto.Name(), addr)
	if res.Version != "" {
		msg += " (" + res.Version + ")"
	}
	ok := true
	unavailable := false
	if res.Failure != "" {
		ok, msg = false, fmt.Sprintf("%s %s: %s", c.proto.Name(), addr, res.Failure)
	} else if fail, missing := c.evalExpect(res); fail != "" {
		ok, msg = false, fmt.Sprintf("%s %s: %s", c.proto.Name(), addr, fail)
		unavailable = missing
	}
	if ok && c.latencyAssertion.op != "" {
		ms := strconv.FormatInt(elapsed.Milliseconds(), numericBaseDecimal)
		pass, lerr := c.latencyAssertion.compare(ms)
		switch {
		case lerr != nil:
			ok, msg = false, fmt.Sprintf("%s %s: latency: %v", c.proto.Name(), addr, lerr)
		case !pass:
			ok, msg = false, fmt.Sprintf("%s %s: latency %sms not %s %s", c.proto.Name(), addr, ms, c.latencyAssertion.op, c.latencyAssertion.value)
		}
	}
	return ok, msg, unavailable
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

// expectField returns the probe value an expect assertion reads: "version" (the
// Result.Version) or a key of Result.Extra.
func expectField(res conn.Result, path string) (string, bool) {
	if path == DataKeyVersion {
		return res.Version, true
	}
	v, ok := res.Extra[path]
	return v, ok
}

// gradeExpectResult raises a failed check to the highest tier one of whose looser
// assertions fails too. A field the probe did not return, or one a tier cannot
// compare (not a number), never escalates: the base failure already reports it.
func gradeExpectResult(r Result, tiers grades[[]jsonAssertion], res conn.Result) Result {
	if len(tiers) == 0 || r.Observation() != ObservationFailing {
		return r
	}
	return raiseSeverity(r, tiers.highest(func(assertions []jsonAssertion) bool {
		for _, a := range assertions {
			got, ok := expectField(res, a.path)
			if !ok {
				continue
			}
			if pass, err := a.compare(got); err == nil && !pass {
				return true
			}
		}
		return false
	}))
}

// evalExpect checks every configured assertion against the probe result and
// returns the first failure ("" when all hold or none are configured), plus
// whether the value needed to evaluate it was unavailable. A field is "version"
// (the Result.Version) or a key of Result.Extra.
func (c connCheck) evalExpect(res conn.Result) (string, bool) {
	for _, a := range c.expect {
		got, ok := expectField(res, a.path)
		if !ok {
			return fmt.Sprintf("field %q not available", a.path), true
		}
		ok, err := a.compare(got)
		if err != nil {
			return fmt.Sprintf("%s: %v", a.path, err), true
		}
		if !ok {
			return fmt.Sprintf("%s %q %s %q not satisfied", a.path, got, a.op, a.value), false
		}
	}
	return "", false
}

// buildConnCheck builds a connection-protocol check for a registered protocol.
// The password arrives already resolved from ${env:...} by the config loader.
func buildConnCheck(b base, proto conn.Protocol, entry map[string]any) (Check, string) {
	protoName := proto.Name()
	cfg := databaseConnectionConfig(entry)
	if cfg.User == "" && proto.RequiresUser() {
		return nil, protoName + " check requires a user"
	}
	cfg.Port = connectionPort(entry, 0)
	cfg.Socket = cfgval.AsString(entry[CheckKeySocket])
	cfg.Query = cfgval.AsString(entry[CheckKeyQuery])
	// cfg.Interface is set per-attempt by connCheck.Run from the interface set;
	// it pins the probe's egress (SO_BINDTODEVICE) on multi-homed hosts.
	if err := configureConnProtocol(&cfg, protoName, entry); err != nil {
		return nil, err.Error()
	}
	cfg = conn.Resolve(proto, cfg)
	c := connCheck{base: b, proto: proto, cfg: cfg}
	// Optional response assertions: a mapping of field -> value | {op, value},
	// compared against the probe Result (version / Extra) — works for any protocol.
	expect, ewarn := parseAssertionMap(entry[CheckKeyExpect], CheckKeyExpect)
	if ewarn != "" {
		return nil, protoName + " check: " + ewarn
	}
	c.expect = expect
	c.expectGrades = parseGrades(b.levels, func(tier map[string]any) ([]jsonAssertion, bool) {
		assertions, warn := parseAssertionMap(tier[CheckKeyExpect], CheckKeyExpect)
		return assertions, warn == "" && len(assertions) > 0
	})
	lop, lval, lwarn := parseExpectLatency(entry)
	if lwarn != "" {
		return nil, protoName + " check: " + lwarn
	}
	c.latencyAssertion = newValueMatcher(lop, lval)
	increases, window, iwarnMax := parseConnIncreases(entry)
	if iwarnMax != "" {
		return nil, protoName + " check: " + iwarnMax
	}
	c.increases, c.window = increases, window
	c.onChange = cfgval.Bool(entry[CheckKeyOnChange])
	c.onVersionChange = cfgval.Bool(entry[CheckKeyOnVersionChange])
	if c.onChange || c.onVersionChange {
		c.state = &connState{}
	}
	c.ifaces = cfgval.StringList(entry[CheckKeyInterface])
	all, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, protoName + " check: " + iwarn
	}
	c.ifaceAll = all
	return c, ""
}

// parseConnIncreases parses max_increase (field -> non-negative integer; 0
// fails on any rise) and its within span. Growth is measured over wall-clock
// time, not cycles, so one requires the other — the same contract as the
// strays check.
func parseConnIncreases(entry map[string]any) ([]connIncrease, time.Duration, string) {
	raw, present := entry[CheckKeyMaxIncrease]
	if !present {
		if _, hasWindow := entry[CheckKeyWithin]; hasWindow {
			return nil, 0, CheckKeyWithin + " requires " + CheckKeyMaxIncrease
		}
		return nil, 0, ""
	}
	m, ok := raw.(map[string]any)
	if !ok || len(m) == 0 {
		return nil, 0, CheckKeyMaxIncrease + " must be a mapping of field -> non-negative integer"
	}
	window := cfgval.Duration(entry[CheckKeyWithin])
	if window <= 0 {
		return nil, 0, CheckKeyMaxIncrease + " requires " + CheckKeyWithin + " as a positive duration"
	}
	out := make([]connIncrease, 0, len(m))
	for _, field := range slices.Sorted(maps.Keys(m)) {
		n, ok := cfgval.Int(m[field])
		if !ok || n < 0 {
			return nil, 0, CheckKeyMaxIncrease + "." + field + " must be a non-negative integer"
		}
		out = append(out, connIncrease{field: field, limit: float64(n), state: &counterWindow{}})
	}
	return out, window, ""
}

func baseConnectionConfig(entry map[string]any) conn.Config {
	cfg := conn.Config{
		Host:     cfgval.AsString(entry[CheckKeyHost]),
		User:     cfgval.AsString(entry[CheckKeyUser]),
		Password: cfgval.AsString(entry[CheckKeyPassword]),
		TLS:      tlsString(entry[CheckKeyTLS]),
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

func preparedConnectionConfig(protocol string, cfg conn.Config, entry map[string]any) conn.Config {
	cfg.Port = connectionPort(entry, 0)
	if _, resolved, ok := conn.Prepare(protocol, cfg); ok {
		return resolved
	}
	return cfg
}

func configureConnProtocol(cfg *conn.Config, protoName string, entry map[string]any) error {
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
	case conn.ProtocolNameSMTPAcceptance:
		return configureSMTPAcceptance(cfg, entry)
	case conn.ProtocolNameFPM:
		setConnQuery(cfg, entry, CheckKeyStatusPath)
	case conn.ProtocolNameNUT:
		setConnQuery(cfg, entry, CheckKeyUPS)
	case conn.ProtocolNameDocker:
		setConnQuery(cfg, entry, CheckKeyContainer)
	case conn.ProtocolNameLibvirt:
		setConnParam(cfg, conn.ParamKeyDomain, cfgval.AsString(entry[CheckKeyDomain]))
	case conn.ProtocolNameDBus:
		cfg.Socket = conn.DBusAddress(cfgval.AsString(entry[CheckKeySocket]), cfgval.AsString(entry[CheckKeyQuery]))
		for _, field := range DBusTargetStringFields() {
			setConnParam(cfg, field, cfgval.AsString(entry[field]))
		}
		if cfgval.Bool(entry[CheckKeyDBusRequireOwner]) {
			setConnParam(cfg, conn.ParamKeyDBusRequireOwner, conn.ParamValueTrue)
		}
		if err := conn.ValidateDBusTarget(DBusTargetFromEntry(entry)); err != nil {
			return fmt.Errorf("dbus check: %w", err)
		}
	case conn.ProtocolNameAvahi:
		cfg.Socket = conn.DBusAddress(cfgval.AsString(entry[CheckKeySocket]), cfgval.AsString(entry[CheckKeyQuery]))
	}
	return nil
}

func configureSMTPAcceptance(cfg *conn.Config, entry map[string]any) error {
	for _, field := range SMTPAcceptanceUnsupportedFields() {
		if _, present := entry[field]; present {
			return fmt.Errorf("%s check does not support %s", conn.ProtocolNameSMTPAcceptance, field)
		}
	}

	envelope, err := conn.ParseSMTPAcceptanceEnvelope(
		cfgval.AsString(entry[CheckKeyHelo]),
		cfgval.AsString(entry[CheckKeyMailFrom]),
		cfgval.AsString(entry[CheckKeyRecipient]),
		cfgval.AsString(entry[CheckKeyStartTLS]),
	)
	if err != nil {
		return fmt.Errorf("%s check: %w", conn.ProtocolNameSMTPAcceptance, err)
	}

	cfg.Host = envelope.RecipientDomain
	setConnParam(cfg, conn.ParamKeySMTPHelo, envelope.Helo)
	setConnParam(cfg, conn.ParamKeySMTPMailFrom, envelope.MailFrom)
	setConnParam(cfg, conn.ParamKeySMTPRecipient, envelope.Recipient)
	setConnParam(cfg, conn.ParamKeySMTPStartTLS, envelope.StartTLS)
	return nil
}

// SMTPAcceptanceUnsupportedFields returns fields whose ordinary connection
// meaning would conflict with the recipient-derived MX target and STARTTLS
// policy of an SMTP acceptance check.
func SMTPAcceptanceUnsupportedFields() [7]string {
	return [7]string{
		CheckKeyHost, CheckKeySocket, CheckKeyUser, CheckKeyPassword,
		CheckKeyDatabase, CheckKeyQuery, CheckKeyTLS,
	}
}

// DBusTargetStringFields returns the string-valued YAML fields that define a
// named D-Bus target. It returns an array copy so callers cannot alter the
// canonical check schema. require_owner is validated and copied separately as
// a boolean.
func DBusTargetStringFields() [5]string {
	return [5]string{
		CheckKeyDBusBusName,
		CheckKeyDBusObjectPath,
		CheckKeyDBusProbe,
		CheckKeyDBusInterface,
		CheckKeyDBusProperty,
	}
}

// DBusTargetFromEntry reads the named D-Bus target from a check entry. Field
// type errors remain the configuration validator's responsibility.
func DBusTargetFromEntry(entry map[string]any) conn.DBusTarget {
	return conn.DBusTarget{
		BusName:       cfgval.AsString(entry[CheckKeyDBusBusName]),
		ObjectPath:    cfgval.AsString(entry[CheckKeyDBusObjectPath]),
		Probe:         cfgval.AsString(entry[CheckKeyDBusProbe]),
		DBusInterface: cfgval.AsString(entry[CheckKeyDBusInterface]),
		Property:      cfgval.AsString(entry[CheckKeyDBusProperty]),
		RequireOwner:  cfgval.Bool(entry[CheckKeyDBusRequireOwner]),
	}
}

func configureDNS(cfg *conn.Config, entry map[string]any) error {
	if qtype := cfgval.AsString(entry[CheckKeyQType]); qtype != "" {
		_, name, err := conn.ParseDNSQType(qtype)
		if err != nil {
			return fmt.Errorf("dns check: %w", err)
		}
		setConnParam(cfg, conn.ParamKeyQType, name)
	}
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
	if value == "" {
		return
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	cfg.Params[key] = value
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
