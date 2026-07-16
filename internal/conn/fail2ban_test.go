package conn

import "testing"

func TestFail2banProbe(t *testing.T) {
	assertUnixSocketProbe(t, "fail2ban.sock", fail2banProtocol.Probe)
}
