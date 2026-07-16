package conn

import (
	"testing"
)

func TestAsteriskGreetingVersion(t *testing.T) {
	v, ok := asteriskGreetingVersion("Asterisk Call Manager/2.10.6")
	if !ok || v != "2.10.6" {
		t.Fatalf("got %q/%v, want 2.10.6/true", v, ok)
	}
	if _, ok := asteriskGreetingVersion("220 mail ESMTP"); ok {
		t.Fatal("a non-AMI greeting must be rejected")
	}
}

func TestAsteriskProbeAgainstFakeServer(t *testing.T) {
	port := serveBanner(t, "Asterisk Call Manager/8.0.0\r\n", nil)
	assertProbeVersion(t, asteriskProtocol{}, port, "8.0.0", "banner", "Asterisk Call Manager/8.0.0")
}
