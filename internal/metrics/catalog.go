package metrics

// Scope identifies the collector snapshot that publishes a metric.
type Scope string

const (
	// ScopeService identifies metrics sampled from one service process tree.
	ScopeService Scope = "service"
	// ScopeSystem identifies metrics sampled from the whole host.
	ScopeSystem Scope = "system"
)

// Descriptor describes the threshold forms a collector metric publishes.
// Config validation consumes the same catalog as runtime metric sampling so a
// new metric cannot be accepted under a different scope or value form.
type Descriptor struct {
	Name       string
	Absolute   bool
	Percentage bool
}

var descriptors = map[Scope]map[string]Descriptor{
	ScopeService: descriptorMap(
		Descriptor{Name: MetricMemory, Absolute: true, Percentage: true},
		Descriptor{Name: MetricSwap, Absolute: true, Percentage: true},
		Descriptor{Name: MetricProcessCount, Absolute: true},
		Descriptor{Name: MetricFds, Absolute: true},
		Descriptor{Name: MetricThreads, Absolute: true},
		Descriptor{Name: MetricCPU, Percentage: true},
		Descriptor{Name: MetricCPUThread, Percentage: true},
		Descriptor{Name: MetricIORead, Absolute: true},
		Descriptor{Name: MetricIOWrite, Absolute: true},
		Descriptor{Name: MetricIO, Absolute: true},
	),
	ScopeSystem: descriptorMap(
		Descriptor{Name: MetricTotalCPU, Percentage: true},
		Descriptor{Name: MetricTotalMemory, Absolute: true, Percentage: true},
		Descriptor{Name: MetricTotalSwap, Absolute: true, Percentage: true},
		Descriptor{Name: MetricLoad1, Absolute: true},
		Descriptor{Name: MetricLoad5, Absolute: true},
		Descriptor{Name: MetricLoad15, Absolute: true},
	),
}

func descriptorMap(values ...Descriptor) map[string]Descriptor {
	out := make(map[string]Descriptor, len(values))
	for _, descriptor := range values {
		out[descriptor.Name] = descriptor
	}
	return out
}

// LookupDescriptor returns the canonical descriptor for name in scope.
func LookupDescriptor(scope, name string) (Descriptor, bool) {
	byName, ok := descriptors[Scope(scope)]
	if !ok {
		return Descriptor{}, false
	}
	descriptor, ok := byName[name]
	return descriptor, ok
}

// ValidScope reports whether scope names a collector snapshot.
func ValidScope(scope string) bool {
	_, ok := descriptors[Scope(scope)]
	return ok
}
