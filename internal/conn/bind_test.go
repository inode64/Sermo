package conn

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestBindDialerNoInterface(t *testing.T) {
	// No interface -> a plain dialer (Control unset); a normal dial succeeds.
	d := BindDialer("")
	if d.Control != nil {
		t.Fatal("BindDialer(\"\") must not set a Control hook")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := d.DialContext(ctx, "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("plain dial: %v", err)
	}
	_ = c.Close()
}

func TestBindDialerBadInterface(t *testing.T) {
	// A non-existent interface (or no CAP_NET_RAW) makes the dial fail rather than
	// silently egress the wrong link.
	if BindDialer("eth0").Control == nil {
		t.Fatal("BindDialer with an interface must set a Control hook")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if c, err := BindDialer("sermo-nonexistent0").DialContext(ctx, "tcp", ln.Addr().String()); err == nil {
		_ = c.Close()
		t.Fatal("dialing bound to a bogus interface must fail")
	}
}
