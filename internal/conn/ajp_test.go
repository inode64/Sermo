package conn

import (
	"bytes"
	"testing"
)

func TestBuildAJPCPing(t *testing.T) {
	// 0x12 0x34 (web-server->container magic), length 1, prefix 0x0A (CPing).
	want := []byte{0x12, 0x34, 0x00, 0x01, 0x0A}
	if !bytes.Equal(buildAJPCPing(), want) {
		t.Fatalf("CPing = % x, want % x", buildAJPCPing(), want)
	}
}

func TestParseAJPResponse(t *testing.T) {
	// Valid CPong: "AB" magic, length 1, prefix 0x09.
	prefix, err := parseAJPResponse([]byte{0x41, 0x42, 0x00, 0x01, 0x09})
	if err != nil || prefix != 0x09 {
		t.Fatalf("CPong parse: prefix=%#x err=%v", prefix, err)
	}
	if ajpIsCPong(prefix) == false {
		t.Fatal("0x09 must be recognized as CPong")
	}
	// Wrong magic.
	if _, err := parseAJPResponse([]byte{0x00, 0x00, 0x00, 0x01, 0x09}); err == nil {
		t.Fatal("a non-AB magic must error")
	}
	// Too short / truncated payload.
	if _, err := parseAJPResponse([]byte{0x41, 0x42}); err == nil {
		t.Fatal("a short response must error")
	}
	if _, err := parseAJPResponse([]byte{0x41, 0x42, 0x00, 0x05, 0x09}); err == nil {
		t.Fatal("a payload shorter than the declared length must error")
	}
}
