package checks

import (
	"slices"
	"strings"

	"sermo/internal/cfgval"
)

// buildNetCheck builds a network-interface state/speed/errors check.
func buildNetCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	iface := cfgval.AsString(entry[CheckKeyInterface])
	if iface == "" {
		return nil, "net check requires an interface"
	}
	metric := cfgval.AsString(entry[CheckKeyMetric])
	c := &netCheck{base: b, iface: iface, metric: metric, sampler: deps.NetSampler}
	switch metric {
	case NetMetricState:
		expect, warn := parseExpectedMetric(entry, "net state", NetStateSummary, NetStateUp, NetStateDown)
		if warn != "" {
			return nil, warn
		}
		c.expect = expect
	case NetMetricSpeed:
		if cfgval.AsString(entry[CheckKeyOn]) != OnModeChange {
			return nil, "net speed requires on: change"
		}
	case NetMetricErrors:
		c.counters = cfgval.StringArray(entry[CheckKeyCounters])
		if len(c.counters) == 0 {
			c.counters = []string{NetCounterRXErrors, NetCounterTXErrors}
		}
		op, v, errs := parseDeltaThreshold(entry[CheckKeyDelta], "net errors")
		if errs != "" {
			return nil, errs
		}
		c.op, c.value = op, v
	case NetMetricAddress:
		expect, warn := parseExpectedMetric(entry, "net address", NetAddrSummary, NetAddrPresent, NetAddrAbsent)
		if warn != "" {
			return nil, warn
		}
		c.expect = expect
	default:
		return nil, "net check metric must be " + NetMetricSummary
	}
	return c, ""
}

// buildICMPCheck builds an ICMP ping state/latency check.
func buildICMPCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	host := cfgval.AsString(entry[CheckKeyHost])
	if host == "" {
		return nil, "icmp check requires a host"
	}
	count := DefaultPingCount
	if v, ok := cfgval.Int(entry[CheckKeyCount]); ok {
		if v <= 0 {
			return nil, "icmp count must be a positive integer"
		}
		count = v
	}
	metric := cfgval.AsString(entry[CheckKeyMetric])
	allIf, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, "icmp check: " + iwarn
	}
	c := &icmpCheck{base: b, host: host, ifaces: cfgval.StringList(entry[CheckKeyInterface]), ifaceAll: allIf, count: count, metric: metric, sampler: deps.PingSampler}
	if warn := configureICMPMetric(c, entry); warn != "" {
		return nil, warn
	}
	return c, ""
}

func configureICMPMetric(check *icmpCheck, entry map[string]any) string {
	switch check.metric {
	case NetMetricState:
		return configureICMPState(check, entry)
	case IcmpMetricLatency:
		return configureICMPLatency(check, entry)
	default:
		return "icmp check metric must be " + ICMPMetricSummary
	}
}

func configureICMPState(check *icmpCheck, entry map[string]any) string {
	expect, warn := parseExpectedMetric(entry, "icmp state", NetStateSummary, NetStateUp, NetStateDown)
	check.expect = expect
	return warn
}

func parseExpectedMetric(entry map[string]any, metric, summary string, allowed ...string) (expect, warn string) {
	expect = cfgval.AsString(entry[CheckKeyExpect])
	onChange := cfgval.AsString(entry[CheckKeyOn]) == OnModeChange
	if expect == "" && !onChange {
		return "", metric + " requires expect: " + strings.Join(allowed, "|") + " or on: change"
	}
	if expect != "" && !slices.Contains(allowed, expect) {
		return "", metric + " expect must be " + summary
	}
	return expect, ""
}

func configureICMPLatency(check *icmpCheck, entry map[string]any) string {
	threshold, hasThreshold := entry[CheckKeyThreshold].(map[string]any)
	change, hasChange := entry[CheckKeyChange].(map[string]any)
	if !hasThreshold && !hasChange {
		return "icmp latency requires threshold {op, value} or change {delta}"
	}
	if hasThreshold {
		op := cfgval.AsString(threshold[CheckKeyOp])
		if !cfgval.IsCompareOp(op) {
			return "icmp latency threshold has an invalid op"
		}
		value, err := parseFiniteThreshold(threshold[CheckKeyValue])
		if err != nil {
			return "icmp latency threshold value " + err.Error()
		}
		check.hasThreshold, check.op, check.value = true, op, value
		return ""
	}
	delta, err := parseFiniteThreshold(change[CheckKeyDelta])
	if err != nil {
		return "icmp latency change delta " + err.Error()
	}
	check.delta = delta
	return ""
}

// buildRouteCheck builds a default-route presence check.
func buildRouteCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	family := cfgval.AsString(entry[CheckKeyFamily])
	switch family {
	case "":
		family = FamilyIPv4
	case FamilyIPv4, FamilyIPv6:
	default:
		return nil, "route family must be " + RouteFamilySummary
	}
	return routeCheck{base: b, family: family, iface: cfgval.AsString(entry[CheckKeyInterface]), sampler: deps.RouteSampler}, ""
}
