package conn

import (
	"bytes"
	"context"
	"io"
	"net"
	"strconv"
	"testing"
)

func TestMQTTRegistered(t *testing.T) {
	p, ok := Lookup("mqtt")
	if !ok {
		t.Fatal("mqtt not registered")
	}
	if p.DefaultPort() != 1883 {
		t.Fatalf("default port = %d, want 1883", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("mqtt must not require a user")
	}
}

func TestBuildMQTTConnect(t *testing.T) {
	pkt := buildMQTTConnect("sermo-check", "", "")
	if pkt[0] != 0x10 {
		t.Fatalf("control packet = 0x%02x, want 0x10 (CONNECT)", pkt[0])
	}
	if !bytes.Contains(pkt, []byte("MQTT")) {
		t.Fatal("CONNECT must carry the MQTT protocol name")
	}
	if !bytes.Contains(pkt, []byte("sermo-check")) {
		t.Fatal("CONNECT must carry the client id")
	}

	// With credentials, the username/password flags are set.
	auth := buildMQTTConnect("c", "user", "pass")
	if !bytes.Contains(auth, []byte("user")) || !bytes.Contains(auth, []byte("pass")) {
		t.Fatal("authenticated CONNECT must carry user and pass")
	}
}

func TestParseMQTTConnack(t *testing.T) {
	if code, sp, err := parseMQTTConnack([]byte{0x20, 0x02, 0x01, 0x00}); err != nil || code != 0 || !sp {
		t.Fatalf("got code=%d sp=%v err=%v, want 0/true/nil", code, sp, err)
	}
	if code, _, err := parseMQTTConnack([]byte{0x20, 0x02, 0x00, 0x05}); err != nil || code != 5 {
		t.Fatalf("got code=%d err=%v, want 5/nil", code, err)
	}
	if _, _, err := parseMQTTConnack([]byte{0x30, 0x02, 0x00, 0x00}); err == nil {
		t.Fatal("a non-CONNACK packet must error")
	}
	if _, _, err := parseMQTTConnack([]byte{0x20, 0x02}); err == nil {
		t.Fatal("a short CONNACK must error")
	}
}

// serveMQTT accepts one connection, drains the CONNECT and replies with a
// CONNACK carrying returnCode.
func serveMQTT(t *testing.T, returnCode byte) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		buf := make([]byte, 256)
		_, _ = io.ReadAtLeast(c, buf, 1)
		_, _ = c.Write([]byte{0x20, 0x02, 0x00, returnCode})
	}()
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	return port
}

func TestMQTTProbeAccepted(t *testing.T) {
	res, err := mqttProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveMQTT(t, 0)})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra["connack"] != "accepted" {
		t.Fatalf("connack = %q", res.Extra["connack"])
	}
}

func TestMQTTProbeRefused(t *testing.T) {
	if _, err := (mqttProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveMQTT(t, 5)}); err == nil {
		t.Fatal("a non-zero CONNACK return code must fail the probe")
	}
}
