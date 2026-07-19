package checks

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // RFC 6455 mandates SHA-1 for the Sec-WebSocket-Accept key
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
	"sermo/internal/netutil"
)

// wsGUID is the RFC 6455 magic value appended to the client key to derive the
// server's Sec-WebSocket-Accept.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

const (
	wsConnectionUpgrade = "Upgrade"
	wsCRLF              = "\r\n"
	wsDefaultPath       = "/"
	wsDefaultPortPlain  = "80"
	wsDefaultPortTLS    = defaultTLSPort
	wsHeaderFormat      = "%s: %s" + wsCRLF
	wsHeaderAccept      = "Sec-WebSocket-Accept"
	wsHeaderConnection  = "Connection"
	wsHeaderHost        = "Host"
	wsHeaderKey         = "Sec-WebSocket-Key"
	wsHeaderOrigin      = "Origin"
	wsHeaderProtocol    = "Sec-WebSocket-Protocol"
	wsHeaderUpgrade     = "Upgrade"
	wsHeaderVersion     = "Sec-WebSocket-Version"
	wsKeySize           = 16
	wsRequestLineFormat = "GET %s HTTP/1.1" + wsCRLF
	wsUpgradeWebSocket  = "websocket"
	wsVersion13         = "13"
)

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
	ifaces      []string
	ifaceAll    bool
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

	var chosenRes Result
	chosen, perIface, perr := tryInterfaces(c.ifaces, c.ifaceAll, func(iface string) error {
		r := c.handshake(ctx, iface, start)
		chosenRes = r
		if !r.OK {
			return errors.New(r.Message)
		}
		return nil
	})
	if perr != nil {
		r := c.result(false, chosenRes.Message, start)
		r.Data = ifaceData(perIface)
		return r
	}
	chosenRes.Message += ifaceSuffix(chosen)
	if perIface != nil {
		if chosenRes.Data == nil {
			chosenRes.Data = map[string]any{}
		}
		chosenRes.Data[DataKeyInterfaces] = perIface
	}
	return chosenRes
}

// handshake performs the RFC 6455 opening handshake over iface (empty = default
// routing) and returns its Result.
func (c *websocketCheck) handshake(ctx context.Context, iface string, start time.Time) Result {
	addr := net.JoinHostPort(c.host, c.port)
	nc, err := conn.BindDialer(iface).DialContext(ctx, conn.TransportTCP, addr)
	if err != nil {
		return c.result(false, fmt.Sprintf("websocket %s: %v", netutil.RedactURL(c.rawURL), netutil.URLErrorCause(err)), start)
	}
	defer func() { _ = nc.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = nc.SetDeadline(dl)
	}

	if websocketSecure(c.scheme) {
		tc := &tls.Config{ServerName: c.host, MinVersion: tls.VersionTLS12}
		if wsSkipVerify(c.tls) {
			tc.InsecureSkipVerify = true // operator chose tls: skip-verify
		}
		tlsConn := tls.Client(nc, tc)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return c.result(false, fmt.Sprintf("websocket %s: TLS: %v", netutil.RedactURL(c.rawURL), netutil.URLErrorCause(err)), start)
		}
		nc = tlsConn
	}

	key, err := wsKey()
	if err != nil {
		return c.result(false, "websocket: "+err.Error(), start)
	}
	if _, err := nc.Write([]byte(c.handshakeRequest(key))); err != nil {
		return c.result(false, fmt.Sprintf("websocket %s: %v", netutil.RedactURL(c.rawURL), netutil.URLErrorCause(err)), start)
	}

	resp, err := http.ReadResponse(bufio.NewReader(nc), &http.Request{Method: http.MethodGet})
	if err != nil {
		return c.result(false, fmt.Sprintf("websocket %s: %v", netutil.RedactURL(c.rawURL), netutil.URLErrorCause(err)), start)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		return c.result(false, fmt.Sprintf("websocket %s: status %d (want 101)", netutil.RedactURL(c.rawURL), resp.StatusCode), start)
	}
	if got := resp.Header.Get(wsHeaderAccept); got != wsAccept(key) {
		return c.result(false, fmt.Sprintf("websocket %s: invalid Sec-WebSocket-Accept %q", netutil.RedactURL(c.rawURL), got), start)
	}

	res := c.result(true, fmt.Sprintf("websocket %s: 101 Switching Protocols", netutil.RedactURL(c.rawURL)), start)
	res.Data = websocketResponseData(resp)
	return res
}

func websocketResponseData(resp *http.Response) map[string]any {
	return map[string]any{DataKeyStatus: resp.StatusCode, DataKeySubprotocol: resp.Header.Get(wsHeaderProtocol)}
}

// handshakeRequest builds the RFC 6455 client opening handshake.
func (c *websocketCheck) handshakeRequest(key string) string {
	var b strings.Builder
	fmt.Fprintf(&b, wsRequestLineFormat, c.path)
	fmt.Fprintf(&b, wsHeaderFormat, wsHeaderHost, c.host)
	fmt.Fprintf(&b, wsHeaderFormat, wsHeaderUpgrade, wsUpgradeWebSocket)
	fmt.Fprintf(&b, wsHeaderFormat, wsHeaderConnection, wsConnectionUpgrade)
	fmt.Fprintf(&b, wsHeaderFormat, wsHeaderKey, key)
	fmt.Fprintf(&b, wsHeaderFormat, wsHeaderVersion, wsVersion13)
	if c.origin != "" {
		fmt.Fprintf(&b, wsHeaderFormat, wsHeaderOrigin, c.origin)
	}
	if c.subprotocol != "" {
		fmt.Fprintf(&b, wsHeaderFormat, wsHeaderProtocol, c.subprotocol)
	}
	for k, v := range c.headers {
		fmt.Fprintf(&b, wsHeaderFormat, k, v)
	}
	b.WriteString(wsCRLF)
	return b.String()
}

// buildWebsocketCheck parses the url and builds a websocket check.
func buildWebsocketCheck(b base, entry map[string]any) (Check, string) {
	raw := cfgval.AsString(entry[CheckKeyURL])
	if raw == "" {
		return nil, "websocket check requires a url"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, "websocket check: invalid url"
	}
	switch u.Scheme {
	case URLSchemeWS, URLSchemeWSS, URLSchemeHTTP, URLSchemeHTTPS:
	default:
		return nil, "websocket check url scheme must be " + WebsocketURLSchemeSummary
	}
	secure := websocketSecure(u.Scheme)
	port := u.Port()
	if port == "" {
		port = websocketDefaultPort(secure)
	}
	wsAll, iwarn := parseInterfaceMatch(entry)
	if iwarn != "" {
		return nil, "websocket check: " + iwarn
	}
	return &websocketCheck{
		base:        b,
		rawURL:      raw,
		scheme:      u.Scheme,
		host:        u.Hostname(),
		ifaces:      parseInterfaces(entry[CheckKeyInterface]),
		ifaceAll:    wsAll,
		port:        port,
		path:        websocketPath(u),
		tls:         tlsString(entry[CheckKeyTLS]),
		origin:      cfgval.AsString(entry[CheckKeyOrigin]),
		subprotocol: cfgval.AsString(entry[CheckKeySubprotocol]),
		headers:     cfgval.StringMap(entry[CheckKeyHeaders]),
	}, ""
}

func websocketSecure(scheme string) bool {
	return scheme == URLSchemeWSS || scheme == URLSchemeHTTPS
}

func websocketDefaultPort(secure bool) string {
	if secure {
		return wsDefaultPortTLS
	}
	return wsDefaultPortPlain
}

func websocketPath(u *url.URL) string {
	path := u.RequestURI()
	if path == "" {
		return wsDefaultPath
	}
	return path
}

// wsKey returns a fresh base64 Sec-WebSocket-Key (16 random bytes).
func wsKey() (string, error) {
	var b [wsKeySize]byte
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
	case conn.TLSModeSkipVerify:
		return true
	default:
		return false
	}
}
