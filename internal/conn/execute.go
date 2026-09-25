package conn

import "context"

// executeProbe is the single runtime entry point for registered protocols.
// Target defaults are canonical here, including direct Lookup(...).Probe calls.
// Builders also resolve targets for reporting before the first cycle.
// Concrete Probe methods remain private wire implementations; callers receive
// registeredProtocol from Lookup and therefore cannot bypass this preparation.
func executeProbe(ctx context.Context, registration protocolRegistration, cfg Config) (Result, error) {
	cfg = resolveRegistration(registration, cfg)
	//nolint:wrapcheck // Wire implementations already provide protocol/step context; the executor must preserve their user-facing error unchanged.
	return registration.protocol.Probe(ctx, cfg)
}
