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
