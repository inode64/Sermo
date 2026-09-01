package checks

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

const (
	// ClockMinStratum is the lowest healthy NTP stratum accepted by the clock check.
	ClockMinStratum = 1
	// ClockMaxStratum is the highest synchronized NTP stratum accepted by the clock check.
	ClockMaxStratum = 15

	clockSecondsPrecision = 3
	clockMSPrecision      = 3
)

// Clock failure codes name why a clock sample was rejected, published as
// Result.Data["clock_failure"] on a failing check (and so as SERMO_CLOCK_FAILURE
// to a hook). A forced correction acts only on ClockFailureOffset: it is the
// only one a clock step can fix.
const (
	ClockFailureUnsynchronized = "unsynchronized"
	ClockFailureOffset         = "offset"
	ClockFailureStratum        = "stratum"
	ClockFailureRootDispersion = "root_dispersion"
)

// Time sources the clock check can measure drift against. The names are the
// connection protocol names, so the configured source is also the probe lookup.
const (
	// ClockSourceNTP queries the configured remote NTP servers (the default).
	ClockSourceNTP = conn.ProtocolNameNTP
	// ClockSourceChrony reads the local chronyd's own view of the clock over its
	// command protocol, for hosts where chrony runs as a client and therefore
	// serves no NTP of its own to query.
	ClockSourceChrony = conn.ProtocolNameChrony
	// ClockSourceSummary is the user-facing list of accepted source names.
	ClockSourceSummary = ClockSourceNTP + " or " + ClockSourceChrony
)

// The optional sample fields the clock check carries from the probe into
// Result.Data, by the type they are stored as. Everything after the first group
// in each list comes only from source: chrony — the local daemon's own
// diagnostics — and the copy helpers skip keys a sample does not carry, so an
// ntp sample is unaffected.
var (
	clockStringExtras = []string{
		DataKeyLeap,
		DataKeyReferenceID,
		DataKeyReferenceAddress,
		DataKeyReferenceTime,
		DataKeySynchronized,
	}
	clockFloatExtras = []string{
		DataKeyPrecisionSeconds,
		DataKeyRootDelayMS,
		DataKeyRootDispersionMS,
		DataKeyReferenceAgeSecs,
		DataKeyFrequencyPPM,
		DataKeyResidualFreqPPM,
		DataKeySkewPPM,
		DataKeyRMSOffsetSeconds,
		DataKeyLastOffsetSeconds,
		DataKeyUpdateIntervalSecs,
		DataKeySources,
		DataKeySourcesOnline,
		DataKeySourcesOffline,
		DataKeySourcesUnresolved,
	}
)

type clockProbeFunc func(context.Context, conn.Config) (conn.Result, error)

type clockCheck struct {
	base
	source string
	// servers holds the remote servers to try in order for source: ntp, and the
	// single local daemon address for source: chrony.
	servers           []string
	port              int
	socket            string
	maxOffset         time.Duration
	maxStratum        int
	maxRootDispersion time.Duration
	ifaces            []string
	ifaceAll          bool
	probe             clockProbeFunc
}

// address renders the probe target for messages and result data.
func (c clockCheck) address(server string) string {
	return targetAddress(c.socket, server, c.port)
}

type clockSample struct {
	server           string
	iface            string
	offsetSeconds    float64
	offsetAbsSeconds float64
	stratum          int
	data             map[string]any
}

func buildClockCheck(b base, entry map[string]any) (Check, string) {
	source := cfgval.AsString(entry[CheckKeySource])
	if source == "" {
		source = ClockSourceNTP
	}
	var servers []string
	var socket string
	switch source {
	case ClockSourceNTP:
		servers = cfgval.StringList(entry[CheckKeyServers])
		if len(servers) == 0 {
			return nil, "clock check requires servers"
		}
		// The mirror of the servers rule below: silently ignoring a socket
		// would look like the local daemon was being read when it is not.
		if _, present := entry[CheckKeySocket]; present {
			return nil, "clock check socket is only valid with source: " + ClockSourceChrony
		}
	case ClockSourceChrony:
		// chrony reads one local daemon, addressed like any other conn check.
		// Rejecting servers outright keeps a misplaced remote list from looking
		// as if it were being queried.
		if _, present := entry[CheckKeyServers]; present {
			return nil, "clock check servers is only valid with source: " + ClockSourceNTP
		}
		servers = []string{cfgval.AsString(entry[CheckKeyHost])}
		socket = cfgval.AsString(entry[CheckKeySocket])
	default:
		return nil, "clock check source must be " + ClockSourceSummary
	}
	maxOffset := cfgval.Duration(entry[CheckKeyMaxOffset])
	if maxOffset <= 0 {
		return nil, "clock check requires max_offset as a positive duration"
	}
	maxStratum := ClockMaxStratum
	if raw, present := entry[CheckKeyMaxStratum]; present {
		n, ok := cfgval.Int(raw)
		if !ok || n < ClockMinStratum || n > ClockMaxStratum {
			return nil, fmt.Sprintf("clock check max_stratum must be an integer in %d..%d", ClockMinStratum, ClockMaxStratum)
		}
		maxStratum = n
	}
	var maxRootDispersion time.Duration
	if raw, present := entry[CheckKeyMaxRootDispersion]; present {
		maxRootDispersion = cfgval.Duration(raw)
		if maxRootDispersion <= 0 {
			return nil, "clock check max_root_dispersion must be a positive duration"
		}
	}
	port := 0
	if raw, present := entry[CheckKeyPort]; present {
		n, ok := cfgval.Int(raw)
		if !ok || n < cfgval.MinTCPPort || n > cfgval.MaxTCPPort {
			return nil, "clock check port must be an integer in " + cfgval.TCPPortRange()
		}
		port = n
	}
	all, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, "clock check: " + iwarn
	}
	target := conn.Config{Port: port, Socket: socket}
	if source == ClockSourceChrony {
		target.Host = servers[0]
	}
	proto, target, ok := conn.Prepare(source, target)
	if !ok {
		return nil, "clock check requires the " + source + " protocol"
	}
	port = target.Port
	if source == ClockSourceChrony {
		servers[0], socket = target.Host, target.Socket
	}
	return clockCheck{
		base:              b,
		source:            source,
		servers:           servers,
		port:              port,
		socket:            socket,
		maxOffset:         maxOffset,
		maxStratum:        maxStratum,
		maxRootDispersion: maxRootDispersion,
		ifaces:            cfgval.StringList(entry[CheckKeyInterface]),
		ifaceAll:          all,
		probe:             proto.Probe,
	}, ""
}

func (c clockCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()
	start := run.start

	// best keeps the closest sample seen so it can be reported when every server
	// fails a threshold, along with the reason it failed — recomputing that from
	// the sample afterwards would just repeat work the loop already did.
	var best *clockSample
	var bestFailure string
	var failures []string
	for _, server := range c.servers {
		sample, err := c.probeServer(ctx, server)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", c.address(server), err))
			continue
		}
		fail, code := c.sampleFailure(sample)
		if fail == "" {
			return c.clockResult(true, c.okMessage(sample), sample, start)
		}
		// Only a failing sample reaches here, so the code belongs to this
		// sample's own data map and needs no separate tracking.
		sample.data[DataKeyClockFailure] = code
		failures = append(failures, fmt.Sprintf("%s: %s", c.address(server), fail))
		if best == nil || sample.offsetAbsSeconds < best.offsetAbsSeconds {
			best, bestFailure = &sample, fail
		}
	}
	if best != nil {
		return c.clockResult(false, c.failureMessage(*best, bestFailure), *best, start)
	}
	return c.unavailableResult("clock: no usable "+c.source+" sample: "+strings.Join(failures, "; "), start)
}

func (c clockCheck) probeServer(ctx context.Context, server string) (clockSample, error) {
	cfg := conn.Config{Host: server, Port: c.port, Socket: c.socket}
	var res conn.Result
	var latency time.Duration
	probe := func(iface string) error {
		cfg.Interface = iface
		start := time.Now()
		r, e := c.probe(ctx, cfg)
		if e == nil {
			res, latency = trimConnResult(r), time.Since(start)
		}
		return e
	}
	// A Unix socket has no egress link, so an interface pin cannot apply to it.
	// Probing once and reporting no interface keeps the result honest: walking
	// the list here would stamp Data with an interface the dial never bound,
	// and with interface_match: all would report every one of them as ok.
	if c.socket != "" {
		if err := probe(""); err != nil {
			return clockSample{}, err
		}
		return c.parseClockSample(server, "", nil, latency, res)
	}
	chosen, perIface, err := tryInterfaces(c.ifaces, c.ifaceAll, probe)
	if err != nil {
		return clockSample{}, err
	}
	return c.parseClockSample(server, chosen, perIface, latency, res)
}

func (c clockCheck) parseClockSample(server, iface string, perIface map[string]any, latency time.Duration, res conn.Result) (clockSample, error) {
	offsetSeconds, err := requiredFloatExtra(res, DataKeyOffsetSeconds)
	if err != nil {
		return clockSample{}, err
	}
	stratum, err := requiredIntExtra(res, DataKeyStratum)
	if err != nil {
		return clockSample{}, err
	}
	offsetAbsSeconds := math.Abs(offsetSeconds)
	data := map[string]any{
		DataKeyProtocol:         c.source,
		DataKeyLatencyMS:        latency.Milliseconds(),
		DataKeyOffsetSeconds:    offsetSeconds,
		DataKeyOffsetAbsSeconds: offsetAbsSeconds,
		DataKeyStratum:          stratum,
		DataKeyValue:            offsetAbsSeconds,
	}
	if c.socket != "" {
		data[DataKeySocket] = c.socket
	} else {
		data[DataKeyServer], data[DataKeyPort] = server, c.port
	}
	if iface != "" {
		data[DataKeyInterface] = iface
	}
	if perIface != nil {
		data[DataKeyInterfaces] = perIface
	}
	for _, key := range clockStringExtras {
		copyStringExtra(data, res, key)
	}
	for _, key := range clockFloatExtras {
		copyFloatExtra(data, res, key)
	}
	return clockSample{
		server:           server,
		iface:            iface,
		offsetSeconds:    offsetSeconds,
		offsetAbsSeconds: offsetAbsSeconds,
		stratum:          stratum,
		data:             data,
	}, nil
}

// sampleFailure reports why a sample is unacceptable: a human reason and a
// stable code. The code exists so an action can tell an offset breach — the one
// failure a forced clock step can fix — from a source that is unsynchronized,
// too distant or too dispersed, where stepping would jump the clock by an
// unknown or zero correction. "" and "" mean the sample is good.
func (c clockCheck) sampleFailure(sample clockSample) (reason, code string) {
	// An unsynchronized source reports stratum 0 and an offset near zero, so it
	// would otherwise look like the best sample available and satisfy every
	// threshold. The ntp probe already rejects those replies itself; chronyd
	// reports the state instead of refusing, so the check owns the rule — and
	// shares conn.Synchronized with the probe so the two cannot diverge.
	leap, _ := sample.data[DataKeyLeap].(string)
	if !conn.Synchronized(sample.stratum, leap) {
		unsynchronized := fmt.Sprintf("source is unsynchronized (stratum %d", sample.stratum)
		if leap != "" {
			unsynchronized += ", leap " + leap
		}
		return unsynchronized + ")", ClockFailureUnsynchronized
	}
	if sample.offsetAbsSeconds > c.maxOffset.Seconds() {
		return fmt.Sprintf("offset %s exceeds max_offset %s",
			formatClockSeconds(sample.offsetSeconds), c.maxOffset), ClockFailureOffset
	}
	if sample.stratum > c.maxStratum {
		return fmt.Sprintf("stratum %d exceeds max_stratum %d",
			sample.stratum, c.maxStratum), ClockFailureStratum
	}
	if c.maxRootDispersion > 0 {
		dispersionMS, ok := sample.data[DataKeyRootDispersionMS].(float64)
		if !ok {
			return "root dispersion is unavailable", ClockFailureRootDispersion
		}
		limitMS := float64(c.maxRootDispersion) / float64(time.Millisecond)
		if dispersionMS > limitMS {
			return fmt.Sprintf("root dispersion %sms exceeds max_root_dispersion %s",
				formatClockMS(dispersionMS), c.maxRootDispersion), ClockFailureRootDispersion
		}
	}
	return "", ""
}

func (c clockCheck) clockResult(ok bool, message string, sample clockSample, start time.Time) Result {
	res := c.result(ok, message, start)
	res.Data = sample.data
	return res
}

func (c clockCheck) okMessage(sample clockSample) string {
	return fmt.Sprintf("clock offset %s via %s%s (stratum %d)",
		formatClockSeconds(sample.offsetSeconds), c.address(sample.server), ifaceSuffix(sample.iface), sample.stratum)
}

// failureMessage renders the reason the loop already computed for sample.
func (c clockCheck) failureMessage(sample clockSample, failure string) string {
	return fmt.Sprintf("clock %s via %s%s", failure, c.address(sample.server), ifaceSuffix(sample.iface))
}

// requiredExtra reads a mandatory Extra value and coerces it; the presence and
// error shape shared by the numeric readers below. want names the expected
// form in the parse-failure message.
func requiredExtra[T any](res conn.Result, key, want string, coerce func(any) (T, bool)) (T, error) {
	var zero T
	raw := strings.TrimSpace(res.Extra[key])
	if raw == "" {
		return zero, fmt.Errorf("%s unavailable", key)
	}
	val, ok := coerce(raw)
	if !ok {
		return zero, fmt.Errorf("%s %q is not %s", key, raw, want)
	}
	return val, nil
}

func requiredFloatExtra(res conn.Result, key string) (float64, error) {
	return requiredExtra(res, key, "numeric", cfgval.Float)
}

func requiredIntExtra(res conn.Result, key string) (int, error) {
	return requiredExtra(res, key, "an integer", cfgval.Int)
}

func copyStringExtra(data map[string]any, res conn.Result, key string) {
	if val := strings.TrimSpace(res.Extra[key]); val != "" {
		data[key] = val
	}
}

func copyFloatExtra(data map[string]any, res conn.Result, key string) {
	if val, ok := cfgval.Float(res.Extra[key]); ok {
		data[key] = val
	}
}

func formatClockSeconds(value float64) string {
	return strconv.FormatFloat(value, floatFormatFixed, clockSecondsPrecision, numericBits64) + "s"
}

func formatClockMS(value float64) string {
	return strconv.FormatFloat(value, floatFormatFixed, clockMSPrecision, numericBits64)
}
