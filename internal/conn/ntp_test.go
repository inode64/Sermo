package conn

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/beevik/ntp"
)

func TestNTPExtraFields(t *testing.T) {
	resp := &ntp.Response{
		Leap:           0, // none
		Precision:      500 * time.Millisecond,
		RootDelay:      500 * time.Millisecond,
		RootDispersion: 250 * time.Millisecond,
		Stratum:        2,
		ReferenceID:    binary.BigEndian.Uint32([]byte{192, 168, 1, 1}), // upstream IPv4
	}
	f := ntpExtraFields(resp)
	if f["leap"] != "none" {
		t.Errorf("leap = %q, want none", f["leap"])
	}
	if f["root_delay_ms"] != "500.000" || f["root_dispersion_ms"] != "250.000" {
		t.Errorf("root delay/disp = %q/%q", f["root_delay_ms"], f["root_dispersion_ms"])
	}
	if f["precision_seconds"] != "0.5" {
		t.Errorf("precision_seconds = %q, want 0.5", f["precision_seconds"])
	}
	if f["reference_id"] != "192.168.1.1" {
		t.Errorf("reference_id = %q, want 192.168.1.1", f["reference_id"])
	}

	// Stratum 1: reference id is an ASCII refclock label.
	resp.Stratum = 1
	resp.Leap = 3 // unsynchronized
	resp.ReferenceID = binary.BigEndian.Uint32([]byte("GPS\x00"))
	f = ntpExtraFields(resp)
	if f["reference_id"] != "GPS" {
		t.Errorf("reference_id = %q, want GPS", f["reference_id"])
	}
	if f["leap"] != "unsynchronized" {
		t.Errorf("leap = %q, want unsynchronized", f["leap"])
	}
}

func TestNTPRefID(t *testing.T) {
	// Stratum >= 2: dotted upstream IPv4.
	if got := ntpRefID(binary.BigEndian.Uint32([]byte{10, 0, 0, 1}), 2); got != "10.0.0.1" {
		t.Errorf("ntpRefID(stratum 2) = %q, want 10.0.0.1", got)
	}
	// Stratum 1: ASCII refclock label, NUL/space trimmed.
	if got := ntpRefID(binary.BigEndian.Uint32([]byte("PPS\x00")), 1); got != "PPS" {
		t.Errorf("ntpRefID(stratum 1) = %q, want PPS", got)
	}
}

func TestNTPHealthy(t *testing.T) {
	if !ntpHealthy(1) || !ntpHealthy(15) {
		t.Fatal("stratum 1..15 must be healthy")
	}
	if ntpHealthy(0) || ntpHealthy(16) {
		t.Fatal("kiss-o'-death (0) and unsynchronized (16) must not be healthy")
	}
}
