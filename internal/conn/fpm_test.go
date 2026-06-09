package conn

import (
	"bytes"
	"strings"
	"testing"
)

func TestFPMRegistered(t *testing.T) {
	p, ok := Lookup("fpm")
	if !ok {
		t.Fatal("fpm not registered")
	}
	if p.DefaultPort() != 9000 {
		t.Fatalf("default port = %d, want 9000", p.DefaultPort())
	}
	if p.RequiresUser() {
		t.Fatal("fpm must not require a user")
	}
}

// fcgiResponse builds a canned FastCGI reply: one STDOUT record with body, then
// END_REQUEST.
func fcgiResponse(t *testing.T, body string) string {
	t.Helper()
	var b bytes.Buffer
	if err := writeFCGIRecord(&b, fcgiStdout, []byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writeFCGIRecord(&b, fcgiEndRequest, []byte{0, 0, 0, 0, 0, 0, 0, 0}); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestFPMHandshakePong(t *testing.T) {
	resp := fcgiResponse(t, "Content-type: text/plain\r\nStatus: 200 OK\r\n\r\npong")
	conn := rw{in: strings.NewReader(resp), out: &bytes.Buffer{}}

	res, err := fpmHandshake(conn, "/ping")
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if res.Extra["ping"] != "pong" {
		t.Fatalf("extra = %v", res.Extra)
	}
	// The request must carry the ping path as SCRIPT_NAME.
	if !strings.Contains(conn.out.String(), "/ping") {
		t.Fatalf("request did not include the ping path: %q", conn.out.String())
	}
}

func TestFPMHandshakeNotPong(t *testing.T) {
	// ping.path not enabled -> FPM returns an access-denied page, no "pong".
	resp := fcgiResponse(t, "Status: 404 Not Found\r\n\r\nAccess denied.")
	conn := rw{in: strings.NewReader(resp), out: &bytes.Buffer{}}
	if _, err := fpmHandshake(conn, "/ping"); err == nil {
		t.Fatal("a response without pong must fail (ping.path likely not enabled)")
	}
}

func TestFCGIParamsRoundTrip(t *testing.T) {
	// A short name/value encodes as 1-byte lengths and round-trips.
	enc := encodeFCGIParams([][2]string{{"SCRIPT_NAME", "/ping"}})
	if enc[0] != byte(len("SCRIPT_NAME")) || enc[1] != byte(len("/ping")) {
		t.Fatalf("length prefixes wrong: %v", enc[:2])
	}
	if !strings.Contains(string(enc), "SCRIPT_NAME/ping") {
		t.Fatalf("encoded params = %q", enc)
	}
}
