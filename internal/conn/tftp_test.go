package conn

import (
	"testing"
)

func TestBuildTFTPReadRequest(t *testing.T) {
	req := buildTFTPReadRequest("boot/pxelinux.0")
	// opcode 1 (RRQ), then filename\0octet\0
	want := append([]byte{0, 1}, []byte("boot/pxelinux.0\x00octet\x00")...)
	if string(req) != string(want) {
		t.Fatalf("RRQ = %q, want %q", req, want)
	}
}

func TestParseTFTPReply(t *testing.T) {
	// DATA: opcode 3, block 1.
	op, _, _, err := parseTFTPReply([]byte{0, 3, 0, 1, 'd', 'a', 't', 'a'})
	if err != nil || op != 3 {
		t.Fatalf("DATA parse: op=%d err=%v", op, err)
	}
	// ERROR: opcode 5, code 1, "File not found".
	data := append([]byte{0, 5, 0, 1}, []byte("File not found\x00")...)
	op, code, msg, err := parseTFTPReply(data)
	if err != nil || op != 5 || code != 1 || msg != "File not found" {
		t.Fatalf("ERROR parse: op=%d code=%d msg=%q err=%v", op, code, msg, err)
	}
	// Too short.
	if _, _, _, err := parseTFTPReply([]byte{0, 5}); err == nil {
		t.Fatal("short reply must error")
	}
}

func TestTFTPResponded(t *testing.T) {
	for _, op := range []int{3, 5, 6} { // DATA, ERROR, OACK
		if !tftpResponded(op) {
			t.Fatalf("opcode %d should count as a valid TFTP reply", op)
		}
	}
	if tftpResponded(1) || tftpResponded(99) {
		t.Fatal("an RRQ/garbage opcode is not a server reply")
	}
}
