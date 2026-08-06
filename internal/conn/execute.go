package conn

import "context"

// probeContext is the common runtime state prepared once for every registered
// protocol invocation. Embedding the caller context preserves cancellation and
// deadlines while making the resolved config and bound target available to the
// shared transport adapters.
type probeContext struct {
	context.Context
	config Config
	target probeTarget
}

func newProbeContext(ctx context.Context, registration protocolRegistration, cfg Config) probeContext {
	cfg = resolveRegistration(registration, cfg)
	return probeContext{
		Context: ctx,
		config:  cfg,
		target:  newProbeTarget(cfg, registration.protocol.DefaultPort()),
	}
}

// executeProbe is the single runtime entry point for registered protocols.
// Concrete Probe methods remain private wire implementations; callers receive
// registeredProtocol from Lookup and therefore cannot bypass this preparation.
func executeProbe(ctx context.Context, registration protocolRegistration, cfg Config) (Result, error) {
	probe := newProbeContext(ctx, registration, cfg)
	return registration.protocol.Probe(probe, probe.config)
}

// probeTargetFor reuses the target prepared by executeProbe when a transport
// adapter receives the unchanged connection settings. Direct implementation
// tests and protocol branches that deliberately alter transport settings keep
// constructing the appropriate target locally.
func probeTargetFor(ctx context.Context, cfg Config, defaultPort int) probeTarget {
	if probe, ok := ctx.(probeContext); ok &&
		probe.target.defaultPort == defaultPort && sameTransportConfig(probe.config, cfg) {
		return probe.target
	}
	return newProbeTarget(cfg, defaultPort)
}

func sameTransportConfig(left, right Config) bool {
	return left.Host == right.Host &&
		left.Port == right.Port &&
		left.Socket == right.Socket &&
		left.TLS == right.TLS &&
		left.Interface == right.Interface
}
