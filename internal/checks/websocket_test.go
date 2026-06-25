package checks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestWSAcceptRFCVector(t *testing.T) {
	// RFC 6455 §1.3 worked example.
	if got := wsAccept("dGhlIHNhbXBsZSBub25jZQ=="); got != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("wsAccept = %q, want s3pPLMBiTxaQ9kYGzzhZRbK+xOo=", got)
	}
}

// wsHandshakeServer answers the WebSocket opening handshake. When accept is
// false it returns a wrong Sec-WebSocket-Accept; status overrides 101.
func wsHandshakeServer(t *testing.T, goodAccept bool, status int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusSwitchingProtocols {
			w.WriteHeader(status)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("no hijacker")
			return
		}
		conn, bufrw, err := hj.Hijack()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		accept := wsAccept(r.Header.Get("Sec-WebSocket-Key"))
		if !goodAccept {
			accept = "wrongaccept"
		}
		_, _ = bufrw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " + accept + "\r\n\r\n")
		_ = bufrw.Flush()
	}))
}

func buildWS(t *testing.T, entry map[string]any) (Check, string) {
	t.Helper()
	entry["type"] = "websocket"
	built, warns := Build(map[string]any{"ws": entry}, Deps{DefaultTimeout: 3 * time.Second})
	if len(warns) > 0 {
		return nil, warns[0]
	}
	return built[0].Check, ""
}

func TestWebsocketHandshakeOK(t *testing.T) {
	srv := wsHandshakeServer(t, true, http.StatusSwitchingProtocols)
	defer srv.Close()
	c, warn := buildWS(t, map[string]any{"url": srv.URL + "/socket"})
	if warn != "" {
		t.Fatal(warn)
	}
	res := c.Run(context.Background())
	if !res.OK {
		t.Fatalf("handshake should pass: %s", res.Message)
	}
	if res.Data["status"] != http.StatusSwitchingProtocols {
		t.Fatalf("data = %v", res.Data)
	}
}

func TestWebsocketHandshakeFailures(t *testing.T) {
	// Wrong Sec-WebSocket-Accept fails.
	bad := wsHandshakeServer(t, false, http.StatusSwitchingProtocols)
	defer bad.Close()
	c, _ := buildWS(t, map[string]any{"url": bad.URL})
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("a wrong Sec-WebSocket-Accept must fail")
	}

	// A plain 200 (no upgrade) fails.
	plain := wsHandshakeServer(t, true, http.StatusOK)
	defer plain.Close()
	c, _ = buildWS(t, map[string]any{"url": plain.URL})
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("a non-101 response must fail")
	}
}

func TestBuildWebsocketCheckErrors(t *testing.T) {
	if _, warn := buildWS(t, map[string]any{}); warn == "" {
		t.Fatal("missing url should warn")
	}
	if _, warn := buildWS(t, map[string]any{"url": "ftp://h/x"}); warn == "" {
		t.Fatal("a non-ws/http scheme should warn")
	}
}

// wsSkipVerify gates TLS certificate verification, so it must return true ONLY
// for the explicit opt-out values — a typo or stray value must keep verification
// on (the safe default).
func TestWsSkipVerify(t *testing.T) {
	for _, v := range []string{"skip-verify", "SKIP-VERIFY", "  skip-verify  "} {
		if !wsSkipVerify(v) {
			t.Errorf("wsSkipVerify(%q) = false, want true (explicit opt-out)", v)
		}
	}
	for _, v := range []string{"", "verify", "true", "1", "yes", "skipverify", "skip verify", "none"} {
		if wsSkipVerify(v) {
			t.Errorf("wsSkipVerify(%q) = true, want false (verification must stay on)", v)
		}
	}
}

func TestWebsocketHandshakeRequestHeaders(t *testing.T) {
	c := &websocketCheck{path: "/chat", host: "h:80", origin: "http://o", subprotocol: "chat"}
	req := c.handshakeRequest("KEY")
	if !strings.Contains(req, "Origin: http://o\r\n") || !strings.Contains(req, "Sec-WebSocket-Protocol: chat\r\n") {
		t.Fatalf("handshake must carry Origin and subprotocol:\n%s", req)
	}
	// Omitted when unset.
	bare := (&websocketCheck{path: "/", host: "h"}).handshakeRequest("KEY")
	if strings.Contains(bare, "Origin:") || strings.Contains(bare, "Sec-WebSocket-Protocol:") {
		t.Fatalf("bare handshake must not carry Origin/subprotocol:\n%s", bare)
	}
}
