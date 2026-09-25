package conn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"sermo/internal/httpx"
	"sermo/internal/netutil"
)

// httpProbeClient returns an HTTP client for connection probes. When iface is
// set it routes TCP dialing through BindDialer so HTTP-based protocols preserve
// the same SO_BINDTODEVICE behavior as raw TCP probes. Every probe gets a
// private transport that closes its connection after one exchange: a bound or
// TLS-configured one is discarded and must not retain an idle connection (and
// the goroutines that own it) until the keep-alive timeout, and a plain one
// must not keep answering over a socket the target accepted before it stopped
// accepting (see httpx.NewProbeClient).
func httpProbeClient(iface string, tlsConfig *tls.Config) *http.Client {
	return httpx.NewProbeClient(httpx.ClientOptions{
		DialContext: BindDialContext(iface),
		TLS:         tlsConfig,
	})
}

// httpProbeBase builds the shared client and base URL for HTTP connection
// probes. Its client always preserves cfg.Interface through httpProbeClient;
// TLS follows the normal probe policy (plaintext by default, or HTTPS with an
// optional operator-selected skip-verify mode).
func httpProbeBase(cfg Config, defaultPort int) (*http.Client, string) {
	return httpProbeBaseWithTLSMode(cfg, defaultPort, netutil.NormalizeTLS(cfg.TLS))
}

// httpProbeBaseWithTLSMode builds an HTTP probe target with an explicit TLS
// policy. Most protocols use httpProbeBase and inherit cfg.TLS; protocols with
// a documented policy (such as UniFi's self-signed HTTPS default) pass their
// resolved mode here without duplicating host/port or interface binding.
func httpProbeBaseWithTLSMode(cfg Config, defaultPort int, tlsMode string) (*http.Client, string) {
	target := newProbeTarget(cfg, defaultPort)
	host, _ := target.hostPort()
	scheme := schemeHTTP
	var tlsConfig *tls.Config
	if tlsMode != "" {
		scheme = schemeHTTPS
		tlsConfig = netutil.TLSClientConfigForMode(host, tlsMode)
	}
	return httpProbeClient(target.cfg.Interface, tlsConfig), scheme + urlSchemeSeparator + target.address()
}

// httpProbeResponse is one bounded HTTP probe exchange: the status code, the
// response headers and the body truncated to the caller's limit.
type httpProbeResponse struct {
	status int
	header http.Header
	body   []byte
}

// doHTTPProbe sends req through client and reads at most limit bytes of the
// response body. Transport errors are returned as-is so each probe applies its
// own protocol prefix; status handling stays with the caller.
func doHTTPProbe(client *http.Client, req *http.Request, limit int64) (httpProbeResponse, error) {
	resp, err := httpx.Do(client, req)
	if err != nil {
		//nolint:wrapcheck // documented above: transport errors stay bare so each probe applies its own probeErr prefix.
		return httpProbeResponse{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return httpProbeResponse{}, fmt.Errorf("read HTTP probe response: %w", err)
	}
	return httpProbeResponse{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// getHTTPProbe builds a GET, optionally decorates its headers, and performs doHTTPProbe.
func getHTTPProbe(ctx context.Context, client *http.Client, url string, limit int64, decorate func(*http.Request)) (httpProbeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		//nolint:wrapcheck // see doHTTPProbe: the caller's probeErr supplies the protocol context.
		return httpProbeResponse{}, err
	}
	if decorate != nil {
		decorate(req)
	}
	return doHTTPProbe(client, req, limit)
}

// decodedJSON reports whether an API reply can be recognized before choosing
// a fallback endpoint. Malformed JSON is a routing signal for these probes.
func decodedJSON(data []byte, out any) bool {
	return json.Unmarshal(data, out) == nil
}

// getJSONProbe performs a bounded GET for probes that require HTTP 200 and JSON.
// Probes with fallback endpoints use getHTTPProbe and decodedJSON instead.
func getJSONProbe(ctx context.Context, client *http.Client, url string, decorate func(*http.Request), out any) error {
	resp, err := getHTTPProbe(ctx, client, url, maxHTTPProbeBody, decorate)
	if err != nil {
		return err
	}
	if resp.status != http.StatusOK {
		return fmt.Errorf("HTTP status %d", resp.status)
	}
	if err := json.Unmarshal(resp.body, out); err != nil {
		return fmt.Errorf("invalid JSON response: %w", err)
	}
	return nil
}
