package conn

import "testing"

func TestAcpidProbe(t *testing.T) {
	assertUnixSocketProbe(t, "acpid.socket", acpidProtocol.Probe)
}
