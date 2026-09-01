package checks

// availabilityTypes are the check types whose verdict is an availability
// statement: they assert that something answers, so "how much of the last day
// did it answer" is a meaningful question and the failing half of it is real
// downtime.
//
// The set is deliberately narrow. Most checks are condition-style — a storage
// watch crossing 90% used, a load watch over its threshold — and their firing
// half is a threshold being met, not an outage. Recording those as downtime
// would make an availability figure that reads like uptime but is not, which is
// worse than not offering one. Adding a type here is a decision that its
// failing half really is unavailability.
var availabilityTypes = map[string]bool{
	CheckTypeTCP:   true,
	CheckTypePorts: true,
	CheckTypeHTTP:  true,
	CheckTypeCert:  true,
	CheckTypeNet:   true,
	CheckTypeICMP:  true,
	CheckTypeRoute: true,
}

// availabilityScope narrows the types that assert availability in some of their
// forms but not others. Without it a single type name would drag unrelated
// measurements into a figure that reads like uptime.
//
// `net` and `icmp` also measure speed, error counters, addresses and latency: a
// link renegotiating to a lower speed or counting a CRC error is not the
// interface being unreachable. `cert` reads either a TLS endpoint or a file on
// disk, and only the endpoint form answers "did it respond" — a certificate file
// approaching expiry is a condition, and recording it as downtime would say a
// host was unavailable for the weeks before someone renewed a PEM.
var availabilityScope = map[string]func(data map[string]any) bool{
	CheckTypeNet:  isLinkStateMetric,
	CheckTypeICMP: isLinkStateMetric,
	CheckTypeCert: isEndpointProbe,
}

func isLinkStateMetric(data map[string]any) bool {
	metric, _ := data[DataKeyMetric].(string)
	return metric == NetMetricState
}

func isEndpointProbe(data map[string]any) bool {
	host, _ := data[DataKeyHost].(string)
	return host != ""
}

// SLAOverride reads a check entry's `sla:` boolean: the operator's own call on
// whether this watch's verdict is an availability series. declared reports
// whether the entry says anything at all; absent, the availabilityTypes default
// decides. It exists because the default set is deliberately narrow — a
// threshold firing is not downtime — but the operator who wrote the threshold
// may know better: a clock offset breach or an unmounted filesystem is downtime
// for whoever depends on it, and that judgement belongs in the YAML, not here.
func SLAOverride(entry map[string]any) (value, declared bool) {
	raw, present := entry[CheckKeySLA]
	if !present {
		return false, false
	}
	b, ok := raw.(bool)
	return b && ok, ok
}

// ConfiguredRecordsAvailability reports whether a configured check keeps an
// availability series. An explicit `sla:` value overrides the type's default
// in both directions. metrics contains sibling multi-metric watch entries;
// result-time callers without configuration should use RecordsAvailability.
func ConfiguredRecordsAvailability(checkType string, entry, metrics map[string]any) bool {
	if value, declared := SLAOverride(entry); declared {
		return value
	}
	if RecordsAvailability(checkType, entry) {
		return true
	}
	for metric := range metrics {
		if RecordsAvailability(checkType, map[string]any{DataKeyMetric: metric}) {
			return true
		}
	}
	return false
}

// RecordsAvailability reports whether one result should contribute to an
// availability series. data is the result's own data map — or, for the callers
// deciding whether a *configured* check keeps a series at all, its check entry,
// which spells `metric` and `host` the same way. A nil map offers neither, which
// for a scoped type is not an availability sample.
func RecordsAvailability(checkType string, data map[string]any) bool {
	if !availabilityTypes[checkType] {
		return false
	}
	if scope, scoped := availabilityScope[checkType]; scoped {
		return scope(data)
	}
	return true
}
