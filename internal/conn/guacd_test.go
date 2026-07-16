package conn

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

func TestGuacInstruction(t *testing.T) {
	if got := guacInstruction("select", "vnc"); got != "6.select,3.vnc;" {
		t.Fatalf("got %q, want 6.select,3.vnc;", got)
	}
	if got := guacInstruction("args"); got != "4.args;" {
		t.Fatalf("got %q, want 4.args;", got)
	}
}

func TestParseGuacInstruction(t *testing.T) {
	if op, err := parseGuacInstruction("4.args,8.hostname,4.port;"); err != nil || op != "args" {
		t.Fatalf("got %q/%v, want args/nil", op, err)
	}
	if op, err := parseGuacInstruction("5.error,21.Protocol \"x\" missing,3.515;"); err != nil || op != "error" {
		t.Fatalf("got %q/%v, want error/nil", op, err)
	}
	if _, err := parseGuacInstruction("garbage"); err == nil {
		t.Fatal("a non-Guacamole reply must error")
	}
	if _, err := parseGuacInstruction("9.args;"); err == nil {
		t.Fatal("a length longer than the data must error")
	}
}

func TestGuacdProbeAgainstFakeServer(t *testing.T) {
	var gotSelect string
	port := serveOnce(t, func(c net.Conn) {
		line, _ := bufio.NewReader(c).ReadString(';')
		gotSelect = line
		_, _ = c.Write([]byte("4.args,8.hostname,4.port;"))
	})
	res, err := guacdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["opcode"] != "args" {
		t.Fatalf("opcode = %q, want args", res.Extra["opcode"])
	}
	if !strings.HasPrefix(gotSelect, "6.select,3.vnc;") {
		t.Fatalf("server received %q, want a select vnc instruction", gotSelect)
	}
}

func TestGuacdProbeCustomProtocol(t *testing.T) {
	port := serveOnce(t, func(c net.Conn) {
		_, _ = bufio.NewReader(c).ReadString(';')
		_, _ = c.Write([]byte("4.args,8.hostname;"))
	})
	// query selects the protocol (e.g. rdp instead of the default vnc).
	res, err := guacdProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port, Query: "rdp"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["select"] != "rdp" {
		t.Fatalf("select = %q, want rdp", res.Extra["select"])
	}
}
