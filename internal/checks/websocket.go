package checks

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6455 mandates SHA-1 for the Sec-WebSocket-Accept key
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// wsGUID is the RFC 6455 magic value appended to the client key to derive the
// server's Sec-WebSocket-Accept.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// websocketCheck verifies a WebSocket endpoint completes the RFC 6455 opening
// handshake: it sends the HTTP Upgrade request and checks the server answers
// 101 Switching Protocols with a Sec-WebSocket-Accept matching the sent key.
// Health-style (OK==true means the handshake succeeded). ws/http use plaintext,
// wss/https use TLS (`tls: skip-verify` to accept a self-signed cert).
type websocketCheck struct {
	base
	rawURL      string
	scheme      string
	host        string
	port        string
	path        string
	tls         string
	origin      string
	subprotocol string
	headers     map[string]string
}

func (c *websocketCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	addr := net.JoinHostPort(c.host, c.port)
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return c.result(false, fmt.Sprintf("websocket %s: %v", c.rawURL, err), start)
	}
	defer func() { _ = conn.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}

	if c.scheme == "wss" || c.scheme == "https" {
		tc := &tls.Config{ServerName: c.host, MinVersion: tls.VersionTLS12}
		if wsSkipVerify(c.tls) {
			tc.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		tlsConn := tls.Client(conn, tc)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return c.result(false, fmt.Sprintf("websocket %s: TLS: %v", c.rawURL, err), start)
		}
		conn = tlsConn
	}

	key, err := wsKey()
	if err != nil {
		return c.result(false, "websocket: "+err.Error(), start)
	}
	if _, err := conn.Write([]byte(c.handshakeRequest(key))); err != nil {
		return c.result(false, fmt.Sprintf("websocket %s: %v", c.rawURL, err), start)
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodGet})
	if err != nil {
		return c.result(false, fmt.Sprintf("websocket %s: %v", c.rawURL, err), start)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return c.result(false, fmt.Sprintf("websocket %s: status %d (want 101)", c.rawURL, resp.StatusCode), start)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != wsAccept(key) {
		return c.result(false, fmt.Sprintf("websocket %s: invalid Sec-WebSocket-Accept %q", c.rawURL, got), start)
	}

	res := c.result(true, fmt.Sprintf("websocket %s: 101 Switching Protocols", c.rawURL), start)
	res.Data = map[string]any{"status": resp.StatusCode, "subprotocol": resp.Header.Get("Sec-WebSocket-Protocol")}
	return res
}

// handshakeRequest builds the RFC 6455 client opening handshake.
func (c *websocketCheck) handshakeRequest(key string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "GET %s HTTP/1.1\r\n", c.path)
	fmt.Fprintf(&b, "Host: %s\r\n", c.host)
	b.WriteString("Upgrade: websocket\r\n")
	b.WriteString("Connection: Upgrade\r\n")
	fmt.Fprintf(&b, "Sec-WebSocket-Key: %s\r\n", key)
	b.WriteString("Sec-WebSocket-Version: 13\r\n")
	if c.origin != "" {
		fmt.Fprintf(&b, "Origin: %s\r\n", c.origin)
	}
	if c.subprotocol != "" {
		fmt.Fprintf(&b, "Sec-WebSocket-Protocol: %s\r\n", c.subprotocol)
	}
	for k, v := range c.headers {
		fmt.Fprintf(&b, "%s: %s\r\n", k, v)
	}
	b.WriteString("\r\n")
	return b.String()
}

// buildWebsocketCheck parses the url and builds a websocket check.
func buildWebsocketCheck(b base, entry map[string]any) (Check, string) {
	raw := asString(entry["url"])
	if raw == "" {
		return nil, "websocket check requires a url"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, "websocket check: invalid url"
	}
	switch u.Scheme {
	case "ws", "wss", "http", "https":
	default:
		return nil, "websocket check url scheme must be ws, wss, http or https"
	}
	secure := u.Scheme == "wss" || u.Scheme == "https"
	port := u.Port()
	if port == "" {
		if secure {
			port = "443"
		} else {
			port = "80"
		}
	}
	path := u.RequestURI()
	if path == "" {
		path = "/"
	}
	return &websocketCheck{
		base:        b,
		rawURL:      raw,
		scheme:      u.Scheme,
		host:        u.Hostname(),
		port:        port,
		path:        path,
		tls:         tlsString(entry["tls"]),
		origin:      asString(entry["origin"]),
		subprotocol: asString(entry["subprotocol"]),
		headers:     stringMap(entry["headers"]),
	}, ""
}

// wsKey returns a fresh base64 Sec-WebSocket-Key (16 random bytes).
func wsKey() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(b[:]), nil
}

// wsAccept computes the expected Sec-WebSocket-Accept for a client key.
func wsAccept(key string) string {
	h := sha1.New() //nolint:gosec // RFC 6455 mandates SHA-1 here
	_, _ = h.Write([]byte(key + wsGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// wsSkipVerify reports whether the tls value requests skipping verification.
func wsSkipVerify(tlsVal string) bool {
	switch strings.ToLower(strings.TrimSpace(tlsVal)) {
	case "skip-verify", "skip_verify", "insecure":
		return true
	default:
		return false
	}
}
