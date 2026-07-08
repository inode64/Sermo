package conn

import (
	"bytes"
	"strings"
	"testing"
)

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

func TestFPMMergeStatus(t *testing.T) {
	stdout := "Content-type: application/json\r\n\r\n" +
		`{"pool":"www","process manager":"dynamic","start since":3600,"accepted conn":42,` +
		`"listen queue":0,"max listen queue":5,"idle processes":8,"active processes":2,` +
		`"total processes":10,"max active processes":4,"max children reached":1,"slow requests":3}`
	extra := map[string]string{"ping": "pong"}
	mergeFPMStatus(extra, stdout)
	want := map[string]string{
		"pool": "www", "process_manager": "dynamic", "active_processes": "2",
		"idle_processes": "8", "total_processes": "10", "listen_queue": "0",
		"max_listen_queue": "5", "max_children_reached": "1", "slow_requests": "3",
		"accepted_conn": "42", "uptime_seconds": "3600",
	}
	for k, v := range want {
		if extra[k] != v {
			t.Errorf("extra[%q] = %q, want %q", k, extra[k], v)
		}
	}

	// A non-JSON body (status path not enabled) must not add metric keys.
	bad := map[string]string{"ping": "pong"}
	mergeFPMStatus(bad, "Status: 404 Not Found\r\n\r\nAccess denied.")
	if len(bad) != 1 {
		t.Fatalf("non-JSON status must leave extra untouched, got %v", bad)
	}
}

func TestFCGIParamsRoundTrip(t *testing.T) {
	// A short name/value encodes as 1-byte lengths and round-trips.
	enc := encodeFCGIParams([]fcgiParam{{"SCRIPT_NAME", "/ping"}})
	if enc[0] != byte(len("SCRIPT_NAME")) || enc[1] != byte(len("/ping")) {
		t.Fatalf("length prefixes wrong: %v", enc[:2])
	}
	if !strings.Contains(string(enc), "SCRIPT_NAME/ping") {
		t.Fatalf("encoded params = %q", enc)
	}
}
