package conn

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"
)

func TestBuildTFTPReadRequest(t *testing.T) {
	req := buildTFTPReadRequest("boot/pxelinux.0")
	// opcode 1 (RRQ), then filename\0octet\0
	want := append([]byte{0, 1}, []byte("boot/pxelinux.0\x00octet\x00")...)
	if !bytes.Equal(req, want) {
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

// serveTFTPTransfer answers the RRQ on a fresh transfer port (TID) like a real
// server: first from spoof (when set), then reply from the TID. It returns the
// server port and a channel with whatever the client sends to the TID next.
func serveTFTPTransfer(t *testing.T, spoof net.PacketConn, reply []byte) (int, <-chan []byte) {
	t.Helper()
	pc, port := listenUDPLoopback(t)
	tid, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tid.Close() })
	next := make(chan []byte, 1)
	go func() {
		buf := make([]byte, 1500)
		_, client, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		if spoof != nil {
			_, _ = spoof.WriteTo([]byte{0, tftpDATA, 0, 1, 'x'}, client)
			time.Sleep(50 * time.Millisecond)
		}
		if _, err := tid.WriteTo(reply, client); err != nil {
			return
		}
		_ = tid.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, _, err := tid.ReadFrom(buf)
		if err != nil {
			close(next)
			return
		}
		next <- append([]byte(nil), buf[:n]...)
	}()
	return port, next
}

func TestTFTPProbeIgnoresRepliesFromOtherHosts(t *testing.T) {
	spoof, err := net.ListenPacket("udp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("second loopback address unavailable: %v", err)
	}
	t.Cleanup(func() { _ = spoof.Close() })
	port, _ := serveTFTPTransfer(t, spoof, buildTFTPError(1, "File not found"))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := (tftpProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Extra[extraReply]; got != tftpOpNameError {
		t.Fatalf("reply = %q, want the server's %q, not the other host's DATA", got, tftpOpNameError)
	}
}

func TestTFTPProbeEndsTransferAfterData(t *testing.T) {
	port, next := serveTFTPTransfer(t, nil, []byte{0, tftpDATA, 0, 1, 'b', 'o', 'o', 't'})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	res, err := (tftpProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port, Query: "pxelinux.0"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Extra[extraReply]; got != tftpOpNameData {
		t.Fatalf("reply = %q, want %q", got, tftpOpNameData)
	}
	abort, ok := <-next
	if !ok {
		t.Fatal("the probe left the transfer open: no ERROR sent to the transfer port")
	}
	if op, code, _, err := parseTFTPReply(abort); err != nil || op != tftpERROR || code != tftpErrorNotDefined {
		t.Fatalf("client sent %v (op %d code %d err %v), want ERROR code 0", abort, op, code, err)
	}
}
