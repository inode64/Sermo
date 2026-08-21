package checks

// identityField is one named string fact about what a target *is*, as opposed
// to what it last measured.
type identityField struct{ key, value string }

// identityNumber is the same for a numeric fact, whose absence is spelled 0.
type identityNumber struct {
	key   string
	value uint64
}

// targetIdentity is implemented by anything that can describe what it is: a
// block device, a network interface. The type owns the mapping from its own
// fields to their data keys, so the writer below stays the single place that
// decides how an identity reaches a result.
type targetIdentity interface {
	identityData() ([]identityField, []identityNumber)
}

// withIdentity adds what a target is to a result's data, creating the map when
// absent.
//
// Every identity field is optional, and the omission is deliberate rather than
// incidental: sysfs, smartctl and the kernel's network attributes each publish a
// different subset, so a bridge has no driver and a SATA disk publishes no
// serial. Writing those as empty rows would read as a value that failed to be
// read rather than one that does not exist — and a zero is a real reading for
// neither a capacity nor an MTU.
func withIdentity(data map[string]any, identity targetIdentity) map[string]any {
	if data == nil {
		data = map[string]any{}
	}
	fields, numbers := identity.identityData()
	for _, field := range fields {
		if field.value != "" {
			data[field.key] = field.value
		}
	}
	for _, number := range numbers {
		if number.value > 0 {
			data[number.key] = number.value
		}
	}
	return data
}
