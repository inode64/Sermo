package checks

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"
)

func TestParsePortSpec(t *testing.T) {
	got, err := parsePortSpec("443,80,1024-1026,80")
	if err != nil {
		t.Fatal(err)
	}
	if want := []int{80, 443, 1024, 1025, 1026}; !reflect.DeepEqual(got, want) {
		t.Fatalf("parsePortSpec = %v, want %v (sorted, de-duplicated)", got, want)
	}
	for _, bad := range []string{"", "0", "70000", "100-50", "abc", "10-"} {
		if _, err := parsePortSpec(bad); err == nil {
			t.Errorf("parsePortSpec(%q) should error", bad)
		}
	}
}

// listenTCP opens a listener and returns its port; caller closes it.
func listenTCP(t *testing.T) (int, net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	return atoi(t, portStr), ln
}

func portsCheckFor(ports []int, expect, match string) *portsCheck {
	return &portsCheck{
		base:           base{name: "p", timeout: time.Second},
		host:           "127.0.0.1",
		ports:          ports,
		expect:         expect,
		match:          match,
		connectTimeout: 300 * time.Millisecond,
	}
}

func TestPortsMatchAllAnyNone(t *testing.T) {
	openPort, ln := listenTCP(t)
	defer ln.Close()
	closedPort, ln2 := listenTCP(t)
	ln2.Close() // now closed
	ports := []int{openPort, closedPort}

	// all open -> fails (one is closed)
	if portsCheckFor(ports, "open", "all").Run(context.Background()).OK {
		t.Error("match=all expect=open should fail when one port is closed")
	}
	// any open -> passes
	if !portsCheckFor(ports, "open", "any").Run(context.Background()).OK {
		t.Error("match=any expect=open should pass with one open port")
	}
	// none open -> fails (one is open)
	if portsCheckFor(ports, "open", "none").Run(context.Background()).OK {
		t.Error("match=none expect=open should fail when a port is open")
	}
	// all closed -> fails (one is open)
	if portsCheckFor(ports, "closed", "all").Run(context.Background()).OK {
		t.Error("match=all expect=closed should fail when a port is open")
	}
	// only the open port, expect all open -> passes
	if !portsCheckFor([]int{openPort}, "open", "all").Run(context.Background()).OK {
		t.Error("a single open port with all/open should pass")
	}
}

func TestPortsOnChange(t *testing.T) {
	port, ln := listenTCP(t)
	c := &portsCheck{
		base:           base{name: "p", timeout: time.Second},
		host:           "127.0.0.1",
		ports:          []int{port},
		expect:         "any", // ignore state expectation; only change matters
		match:          "all",
		onChange:       true,
		connectTimeout: 300 * time.Millisecond,
	}
	// first run primes (open), no change -> OK
	if !c.Run(context.Background()).OK {
		t.Fatal("first run should prime without firing")
	}
	// close the port -> open->closed transition -> not OK
	ln.Close()
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a port state change must make the check fail")
	}
	// stable closed -> OK again
	if !c.Run(context.Background()).OK {
		t.Fatalf("a stable state must not keep firing: %s", c.Run(context.Background()).Message)
	}
}

func TestBuildPortsCheck(t *testing.T) {
	built, warns := Build(map[string]any{
		"scan": map[string]any{"type": "ports", "host": "127.0.0.1", "ports": "80,443,1024-1026", "match": "any"},
	}, Deps{})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("ports check should build: warns=%v built=%d", warns, len(built))
	}
	if _, warns := Build(map[string]any{"bad": map[string]any{"type": "ports", "ports": "nope"}}, Deps{}); len(warns) == 0 {
		t.Fatal("an invalid ports spec should warn")
	}
	if _, warns := Build(map[string]any{"bad": map[string]any{"type": "ports", "ports": "80", "expect": "weird"}}, Deps{}); len(warns) == 0 {
		t.Fatal("an invalid expect should warn")
	}
}
