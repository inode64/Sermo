package conn

import "testing"

func TestUdisks2Registered(t *testing.T) {
	p, ok := Lookup("udisks2")
	if !ok {
		t.Fatal("udisks2 not registered")
	}
	if p.DefaultPort() != 0 {
		t.Fatalf("default port = %d, want 0 (socket-based)", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("udisks2 must not require a user")
	}
}