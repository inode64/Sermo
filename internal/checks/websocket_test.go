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

// The Host header is uri-host[:port] (RFC 9110): a non-default port must be
// kept and an IPv6 literal bracketed, or proxies answer 400 and servers that
// check Origin against Host answer 403.
func TestWebsocketHandshakeHostHeader(t *testing.T) {
	tests := map[string]string{
		"ws://example.test/ws":        "example.test",
		"ws://example.test:80/ws":     "example.test",
		"ws://example.test:8080/ws":   "example.test:8080",
		"wss://example.test:443/ws":   "example.test",
		"https://example.test:8443/x": "example.test:8443",
		"ws://[::1]:8080/ws":          "[::1]:8080",
		"ws://[::1]/ws":               "[::1]",
	}
	for raw, want := range tests {
		c, warn := buildWS(t, map[string]any{"url": raw})
		if warn != "" {
			t.Fatalf("%s: %s", raw, warn)
		}
		ws, ok := c.(*websocketCheck)
		if !ok {
			t.Fatalf("%s: got %T", raw, c)
		}
		if req := ws.handshakeRequest("KEY"); !strings.Contains(req, "\r\nHost: "+want+"\r\n") {
			t.Errorf("%s: handshake Host must be %q:\n%s", raw, want, req)
		}
	}
}
