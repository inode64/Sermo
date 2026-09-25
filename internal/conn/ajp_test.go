package conn

import (
	"bytes"
	"testing"
	"testing/iotest"
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
	prefix, err := parseAJPResponse(bytes.NewReader([]byte{0x41, 0x42, 0x00, 0x01, 0x09}))
	if err != nil || prefix != 0x09 {
		t.Fatalf("CPong parse: prefix=%#x err=%v", prefix, err)
	}
	// Wrong magic.
	if _, err := parseAJPResponse(bytes.NewReader([]byte{0x00, 0x00, 0x00, 0x01, 0x09})); err == nil {
		t.Fatal("a non-AB magic must error")
	}
	// Too short / truncated payload.
	if _, err := parseAJPResponse(bytes.NewReader([]byte{0x41, 0x42})); err == nil {
		t.Fatal("a short response must error")
	}
	if _, err := parseAJPResponse(bytes.NewReader([]byte{0x41, 0x42, 0x00, 0x05, 0x09})); err == nil {
		t.Fatal("a payload shorter than the declared length must error")
	}
}

func TestAJPResponseBoundsAndSplitReads(t *testing.T) {
	for _, tt := range []struct {
		name    string
		packet  []byte
		wantErr bool
	}{
		{name: "split pong", packet: []byte{0x41, 0x42, 0, 1, 9}},
		{name: "empty payload", packet: []byte{0x41, 0x42, 0, 0}, wantErr: true},
		{name: "oversized payload", packet: []byte{0x41, 0x42, 0xff, 0xff}, wantErr: true},
		{name: "truncated payload", packet: []byte{0x41, 0x42, 0, 2, 9}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			prefix, err := parseAJPResponse(iotest.OneByteReader(bytes.NewReader(tt.packet)))
			if (err != nil) != tt.wantErr || (!tt.wantErr && prefix != ajpCPong) {
				t.Fatalf("prefix=%x err=%v", prefix, err)
			}
		})
	}
}
