package conn

import "testing"

// TestProbeMetadata asserts that the immutable built-in catalog resolves every
// canonical name and alias to the declared implementation. The registrations
// themselves are the metadata source, so this test does not duplicate them.
func TestProbeMetadata(t *testing.T) {
	for _, registration := range builtinProtocolRegistrations {
		protocol := registration.protocol
		names := append([]string{protocol.Name()}, registration.aliases...)
		for _, name := range names {
			resolved, ok := Lookup(name)
			if !ok {
				t.Errorf("%s not registered", name)
				continue
			}
			if resolved.Name() != protocol.Name() {
				t.Errorf("%s resolves to %q, want %q", name, resolved.Name(), protocol.Name())
			}
			if _, ok := resolved.(registeredProtocol); !ok {
				t.Errorf("%s resolved to %T, want the common registered-protocol wrapper", name, resolved)
			}
			if resolved.DefaultPort() != protocol.DefaultPort() {
				t.Errorf("%s default port = %d, want %d", name, resolved.DefaultPort(), protocol.DefaultPort())
			}
			if resolved.RequiresUser() != protocol.RequiresUser() {
				t.Errorf("%s RequiresUser = %v, want %v", name, resolved.RequiresUser(), protocol.RequiresUser())
			}
		}
	}
}
