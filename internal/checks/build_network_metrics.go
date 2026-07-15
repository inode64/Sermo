package checks

import (
	"strconv"

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
		expect := cfgval.AsString(entry[CheckKeyExpect])
		onChange := cfgval.AsString(entry[CheckKeyOn]) == OnModeChange
		if expect == "" && !onChange {
			return nil, "net state requires expect: up|down or on: change"
		}
		if expect != "" {
			if expect != NetStateUp && expect != NetStateDown {
				return nil, "net state expect must be " + NetStateSummary
			}
			c.expect = expect
		} else if onChange {
			c.onChange = true
		}
	case NetMetricSpeed:
		if cfgval.AsString(entry[CheckKeyOn]) != OnModeChange {
			return nil, "net speed requires on: change"
		}
		c.onChange = true
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
		expect := cfgval.AsString(entry[CheckKeyExpect])
		onChange := cfgval.AsString(entry[CheckKeyOn]) == OnModeChange
		if expect == "" && !onChange {
			return nil, "net address requires expect: present|absent or on: change"
		}
		if expect != "" {
			if expect != NetAddrPresent && expect != NetAddrAbsent {
				return nil, "net address expect must be " + NetAddrSummary
			}
			c.expect = expect
		} else if onChange {
			c.onChange = true
		}
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
	c := &icmpCheck{base: b, host: host, ifaces: parseInterfaces(entry[CheckKeyInterface]), ifaceAll: allIf, count: count, metric: metric, sampler: deps.PingSampler}
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
	expect := cfgval.AsString(entry[CheckKeyExpect])
	onChange := cfgval.AsString(entry[CheckKeyOn]) == OnModeChange
	if expect == "" && !onChange {
		return "icmp state requires expect: up|down or on: change"
	}
	if expect != "" {
		if expect != NetStateUp && expect != NetStateDown {
			return "icmp state expect must be " + NetStateSummary
		}
		check.expect = expect
		return ""
	}
	check.onChange = true
	return ""
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
		value, err := strconv.ParseFloat(cfgval.String(threshold[CheckKeyValue]), numericBits64)
		if err != nil {
			return "icmp latency threshold value must be numeric"
		}
		check.hasThreshold, check.op, check.value = true, op, value
		return ""
	}
	delta, err := strconv.ParseFloat(cfgval.String(change[CheckKeyDelta]), numericBits64)
	if err != nil {
		return "icmp latency change delta must be numeric"
	}
	check.hasChange, check.delta = true, delta
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
