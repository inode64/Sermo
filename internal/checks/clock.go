package checks

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/netutil"
)

const (
	// ClockMinStratum is the lowest healthy NTP stratum accepted by the clock check.
	ClockMinStratum = 1
	// ClockMaxStratum is the highest synchronized NTP stratum accepted by the clock check.
	ClockMaxStratum = 15

	clockSecondsPrecision = 3
	clockMSPrecision      = 3
)

type ntpProbeFunc func(context.Context, conn.Config) (conn.Result, error)

type clockCheck struct {
	base
	servers           []string
	port              int
	maxOffset         time.Duration
	maxStratum        int
	maxRootDispersion time.Duration
	ifaces            []string
	ifaceAll          bool
	probe             ntpProbeFunc
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
	servers := cfgval.StringList(entry[CheckKeyServers])
	if len(servers) == 0 {
		return nil, "clock check requires servers"
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
	port := conn.DefaultPort(conn.ProtocolNameNTP)
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
	proto, ok := conn.Lookup(conn.ProtocolNameNTP)
	if !ok {
		return nil, "clock check requires the ntp protocol"
	}
	return clockCheck{
		base:              b,
		servers:           servers,
		port:              port,
		maxOffset:         maxOffset,
		maxStratum:        maxStratum,
		maxRootDispersion: maxRootDispersion,
		ifaces:            parseInterfaces(entry[CheckKeyInterface]),
		ifaceAll:          all,
		probe:             proto.Probe,
	}, ""
}

func (c clockCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	var best *clockSample
	var failures []string
	for _, server := range c.servers {
		sample, err := c.probeServer(ctx, server)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", server, err))
			continue
		}
		if best == nil || sample.offsetAbsSeconds < best.offsetAbsSeconds {
			best = &sample
		}
		if fail := c.sampleFailure(sample); fail != "" {
			failures = append(failures, fmt.Sprintf("%s: %s", server, fail))
			continue
		}
		return c.clockResult(true, c.okMessage(sample), sample, start)
	}
	if best != nil {
		return c.clockResult(false, c.failureMessage(*best), *best, start)
	}
	return c.result(false, "clock: all NTP servers failed: "+strings.Join(failures, "; "), start)
}

func (c clockCheck) probeServer(ctx context.Context, server string) (clockSample, error) {
	probe := c.probe
	if probe == nil {
		proto, ok := conn.Lookup(conn.ProtocolNameNTP)
		if !ok {
			return clockSample{}, errors.New("ntp protocol unavailable")
		}
		probe = proto.Probe
	}
	cfg := conn.Config{Host: server, Port: c.port}
	var res conn.Result
	var latency time.Duration
	chosen, perIface, err := tryInterfaces(c.ifaces, c.ifaceAll, func(iface string) error {
		cfg.Interface = iface
		start := time.Now()
		r, e := probe(ctx, cfg)
		if e == nil {
			res, latency = trimConnResult(r), time.Since(start)
		}
		return e
	})
	if err != nil {
		return clockSample{}, err
	}
	sample, err := parseClockSample(server, chosen, perIface, c.port, latency, res)
	if err != nil {
		return clockSample{}, err
	}
	return sample, nil
}

func parseClockSample(server, iface string, perIface map[string]any, port int, latency time.Duration, res conn.Result) (clockSample, error) {
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
		DataKeyServer:           server,
		DataKeyPort:             port,
		DataKeyProtocol:         conn.ProtocolNameNTP,
		DataKeyLatencyMS:        latency.Milliseconds(),
		DataKeyOffsetSeconds:    offsetSeconds,
		DataKeyOffsetAbsSeconds: offsetAbsSeconds,
		DataKeyStratum:          stratum,
		DataKeyValue:            offsetAbsSeconds,
	}
	if iface != "" {
		data[DataKeyInterface] = iface
	}
	if perIface != nil {
		data[DataKeyInterfaces] = perIface
	}
	copyStringExtra(data, res, DataKeyLeap)
	copyStringExtra(data, res, DataKeyReferenceID)
	copyFloatExtra(data, res, DataKeyPrecisionSeconds)
	copyFloatExtra(data, res, DataKeyRootDelayMS)
	copyFloatExtra(data, res, DataKeyRootDispersionMS)
	return clockSample{
		server:           server,
		iface:            iface,
		offsetSeconds:    offsetSeconds,
		offsetAbsSeconds: offsetAbsSeconds,
		stratum:          stratum,
		data:             data,
	}, nil
}

func (c clockCheck) sampleFailure(sample clockSample) string {
	if sample.offsetAbsSeconds > c.maxOffset.Seconds() {
		return fmt.Sprintf("offset %s exceeds max_offset %s", formatClockSeconds(sample.offsetSeconds), c.maxOffset)
	}
	if sample.stratum > c.maxStratum {
		return fmt.Sprintf("stratum %d exceeds max_stratum %d", sample.stratum, c.maxStratum)
	}
	if c.maxRootDispersion > 0 {
		dispersionMS, ok := sample.data[DataKeyRootDispersionMS].(float64)
		if !ok {
			return "root dispersion is unavailable"
		}
		limitMS := float64(c.maxRootDispersion) / float64(time.Millisecond)
		if dispersionMS > limitMS {
			return fmt.Sprintf("root dispersion %sms exceeds max_root_dispersion %s", formatClockMS(dispersionMS), c.maxRootDispersion)
		}
	}
	return ""
}

func (c clockCheck) clockResult(ok bool, message string, sample clockSample, start time.Time) Result {
	res := c.result(ok, message, start)
	res.Data = sample.data
	return res
}

func (c clockCheck) okMessage(sample clockSample) string {
	return fmt.Sprintf("clock offset %s via %s%s (stratum %d)",
		formatClockSeconds(sample.offsetSeconds), netutil.JoinHostPort(sample.server, c.port), ifaceSuffix(sample.iface), sample.stratum)
}

func (c clockCheck) failureMessage(sample clockSample) string {
	if fail := c.sampleFailure(sample); fail != "" {
		return fmt.Sprintf("clock %s via %s%s", fail, netutil.JoinHostPort(sample.server, c.port), ifaceSuffix(sample.iface))
	}
	return "clock has no healthy NTP sample via " + netutil.JoinHostPort(sample.server, c.port)
}

func requiredFloatExtra(res conn.Result, key string) (float64, error) {
	raw, ok := res.Extra[key]
	if !ok || strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("%s unavailable", key)
	}
	val, err := strconv.ParseFloat(strings.TrimSpace(raw), numericBits64)
	if err != nil {
		return 0, fmt.Errorf("%s %q is not numeric", key, raw)
	}
	return val, nil
}

func requiredIntExtra(res conn.Result, key string) (int, error) {
	raw, ok := res.Extra[key]
	if !ok || strings.TrimSpace(raw) == "" {
		return 0, fmt.Errorf("%s unavailable", key)
	}
	val, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s %q is not an integer", key, raw)
	}
	return val, nil
}

func copyStringExtra(data map[string]any, res conn.Result, key string) {
	if val := strings.TrimSpace(res.Extra[key]); val != "" {
		data[key] = val
	}
}

func copyFloatExtra(data map[string]any, res conn.Result, key string) {
	raw := strings.TrimSpace(res.Extra[key])
	if raw == "" {
		return
	}
	if val, err := strconv.ParseFloat(raw, numericBits64); err == nil {
		data[key] = val
	}
}

func formatClockSeconds(value float64) string {
	return strconv.FormatFloat(value, floatFormatFixed, clockSecondsPrecision, numericBits64) + "s"
}

func formatClockMS(value float64) string {
	return strconv.FormatFloat(value, floatFormatFixed, clockMSPrecision, numericBits64)
}
