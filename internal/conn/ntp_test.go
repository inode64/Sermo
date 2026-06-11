package conn

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestNTPRegistered(t *testing.T) {
	p, ok := Lookup("ntp")
	if !ok {
		t.Fatal("ntp not registered")
	}
	if p.DefaultPort() != 123 {
		t.Fatalf("default port = %d, want 123", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("ntp must not require a user")
	}
}

func TestBuildNTPRequest(t *testing.T) {
	req := buildNTPRequest()
	if len(req) != 48 {
		t.Fatalf("request len = %d, want 48", len(req))
	}
	// LI=0, VN=4, Mode=3 (client) -> 0x23.
	if req[0] != 0x23 {
		t.Fatalf("first byte = %#x, want 0x23 (v4 client)", req[0])
	}
}

func TestNTPTimeToUnix(t *testing.T) {
	// 2208988800 NTP seconds == Unix epoch (1970-01-01).
	var b [8]byte
	binary.BigEndian.PutUint32(b[0:], 2208988800)
	binary.BigEndian.PutUint32(b[4:], 0x80000000) // .5 fraction
	got := ntpTimeToUnix(b[:])
	if math.Abs(got-0.5) > 1e-6 {
		t.Fatalf("ntpTimeToUnix = %v, want ~0.5", got)
	}
}

func TestParseNTPResponse(t *testing.T) {
	resp := make([]byte, 48)
	resp[0] = 0x24                                    // LI=0, VN=4, Mode=4 (server)
	resp[1] = 2                                       // stratum
	binary.BigEndian.PutUint32(resp[32:], 2208988900) // T2 = epoch+100s
	binary.BigEndian.PutUint32(resp[40:], 2208988900) // T3
	mode, stratum, t2, t3, err := parseNTPResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	if mode != 4 || stratum != 2 {
		t.Fatalf("mode/stratum = %d/%d", mode, stratum)
	}
	if math.Abs(t2-100) > 1e-6 || math.Abs(t3-100) > 1e-6 {
		t.Fatalf("t2/t3 = %v/%v, want ~100", t2, t3)
	}
	if _, _, _, _, err := parseNTPResponse(make([]byte, 10)); err == nil {
		t.Fatal("a short packet must error")
	}
}

func TestNTPExtraFields(t *testing.T) {
	b := make([]byte, 48)
	b[0] = 0x24                                // LI=0 (none), VN=4, Mode=4
	b[3] = 0xE9                                // precision = int8(0xE9) = -23 -> 2^-23
	binary.BigEndian.PutUint32(b[4:8], 1<<15)  // root delay = 0.5s -> 500ms
	binary.BigEndian.PutUint32(b[8:12], 1<<14) // root dispersion = 0.25s -> 250ms
	copy(b[12:16], []byte{192, 168, 1, 1})     // ref id (stratum >= 2): upstream IPv4
	f := ntpExtraFields(b, 2)
	if f["leap"] != "none" {
		t.Errorf("leap = %q, want none", f["leap"])
	}
	if f["root_delay_ms"] != "500.000" || f["root_dispersion_ms"] != "250.000" {
		t.Errorf("root delay/disp = %q/%q", f["root_delay_ms"], f["root_dispersion_ms"])
	}
	if f["reference_id"] != "192.168.1.1" {
		t.Errorf("reference_id = %q, want 192.168.1.1", f["reference_id"])
	}

	// Stratum 1: reference id is an ASCII refclock label.
	b[0] = 0xC4 // LI=3 (unsynchronized)
	copy(b[12:16], "GPS\x00")
	f = ntpExtraFields(b, 1)
	if f["reference_id"] != "GPS" {
		t.Errorf("reference_id = %q, want GPS", f["reference_id"])
	}
	if f["leap"] != "unsynchronized" {
		t.Errorf("leap = %q, want unsynchronized", f["leap"])
	}
}

func TestNTPHealthy(t *testing.T) {
	if !ntpHealthy(4, 1) || !ntpHealthy(4, 15) {
		t.Fatal("server mode with stratum 1..15 must be healthy")
	}
	if ntpHealthy(3, 2) {
		t.Fatal("non-server mode must not be healthy")
	}
	if ntpHealthy(4, 0) || ntpHealthy(4, 16) {
		t.Fatal("kiss-o'-death (0) and unsynchronized (16) must not be healthy")
	}
}
