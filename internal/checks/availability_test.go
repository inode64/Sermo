package checks

import "testing"

// TestRecordsAvailabilityCoversOnlyReachabilityTypes pins the narrow set. A
// condition check crossing its threshold is a thing to look at, not an outage,
// and recording it as downtime would produce a figure that reads like uptime
// while meaning something else.
func TestRecordsAvailabilityCoversOnlyReachabilityTypes(t *testing.T) {
	for _, checkType := range []string{CheckTypeTCP, CheckTypePorts, CheckTypeHTTP, CheckTypeRoute} {
		if !RecordsAvailability(checkType, nil) {
			t.Errorf("%s records no availability, want it to", checkType)
		}
	}
	for _, checkType := range []string{CheckTypeStorage, CheckTypeLoad, CheckTypeSensors, CheckTypeSmart, CheckTypeFile} {
		if RecordsAvailability(checkType, nil) {
			t.Errorf("%s records availability, want a condition check not to", checkType)
		}
	}
}

// TestRecordsAvailabilityIsMetricScopedForNet keeps a link renegotiating its
// speed or counting a CRC error out of the availability series: neither is the
// interface being unreachable, and folding them in would make the number
// meaningless.
func TestRecordsAvailabilityIsMetricScopedForNet(t *testing.T) {
	for _, checkType := range []string{CheckTypeNet, CheckTypeICMP} {
		if !RecordsAvailability(checkType, map[string]any{DataKeyMetric: NetMetricState}) {
			t.Errorf("%s state metric records no availability, want it to", checkType)
		}
		for _, metric := range []string{NetMetricSpeed, NetMetricErrors, NetMetricAddress, IcmpMetricLatency} {
			if RecordsAvailability(checkType, map[string]any{DataKeyMetric: metric}) {
				t.Errorf("%s %s metric records availability, want only state to", checkType, metric)
			}
		}
		if RecordsAvailability(checkType, nil) {
			t.Errorf("%s with no metric records availability, want a metric to be required", checkType)
		}
	}
}

// TestRecordsAvailabilityIsEndpointScopedForCert keeps a certificate file out of
// the series. Only the endpoint form answers "did it respond"; a PEM approaching
// expiry is a condition, and recording it as downtime would claim a host was
// unavailable for the weeks before someone renewed the file.
func TestRecordsAvailabilityIsEndpointScopedForCert(t *testing.T) {
	if !RecordsAvailability(CheckTypeCert, map[string]any{DataKeyHost: "127.0.0.1"}) {
		t.Error("a cert endpoint probe records no availability, want it to")
	}
	if RecordsAvailability(CheckTypeCert, map[string]any{DataKeyPath: "/etc/ssl/dhparam.pem"}) {
		t.Error("a cert file check records availability, want only the endpoint form to")
	}
	if RecordsAvailability(CheckTypeCert, nil) {
		t.Error("a cert check with no target records availability, want a host to be required")
	}
}
