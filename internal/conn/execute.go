package conn

import "context"

// probeState is the common runtime state prepared once for every registered
// protocol invocation and carried by a context derived from the caller. The
// resolved config and bound target are then available to every transport
// adapter without weakening cancellation or deadline propagation.
type probeState struct {
	config Config
	target probeTarget
}

type probeContextKey struct{}

func newProbeContext(ctx context.Context, registration protocolRegistration, cfg Config) (context.Context, Config) {
	cfg = resolveRegistration(registration, cfg)
	state := probeState{
		config: cfg,
		target: newProbeTarget(cfg, registration.protocol.DefaultPort()),
	}
	return context.WithValue(ctx, probeContextKey{}, state), cfg
}

// executeProbe is the single runtime entry point for registered protocols.
// Concrete Probe methods remain private wire implementations; callers receive
// registeredProtocol from Lookup and therefore cannot bypass this preparation.
func executeProbe(ctx context.Context, registration protocolRegistration, cfg Config) (Result, error) {
	ctx, cfg = newProbeContext(ctx, registration, cfg)
	//nolint:wrapcheck // Wire implementations already provide protocol/step context; the executor must preserve their user-facing error unchanged.
	return registration.protocol.Probe(ctx, cfg)
}

// probeTargetFor reuses the target prepared by executeProbe when a transport
// adapter receives the unchanged connection settings. Direct implementation
// tests and protocol branches that deliberately alter transport settings keep
// constructing the appropriate target locally.
func probeTargetFor(ctx context.Context, cfg Config, defaultPort int) probeTarget {
	if state, ok := ctx.Value(probeContextKey{}).(probeState); ok &&
		state.target.defaultPort == defaultPort && sameTransportConfig(state.config, cfg) {
		return state.target
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
