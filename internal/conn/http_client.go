package conn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"

	"sermo/internal/httpx"
)

// httpProbeClient returns an HTTP client for connection probes. When iface is
// set it routes TCP dialing through BindDialer so HTTP-based protocols preserve
// the same SO_BINDTODEVICE behavior as raw TCP probes.
func httpProbeClient(iface string, tlsConfig *tls.Config) *http.Client {
	if iface == "" && tlsConfig == nil {
		return &http.Client{}
	}
	tr := httpx.CloneDefaultTransport()
	if iface != "" {
		tr.DialContext = BindDialer(iface).DialContext
	}
	if tlsConfig != nil {
		tr.TLSClientConfig = tlsConfig
	}
	return &http.Client{Transport: tr}
}

// httpProbeBase builds the shared client and base URL for HTTP connection
// probes. Its client always preserves cfg.Interface through httpProbeClient;
// TLS follows the normal probe policy (plaintext by default, or HTTPS with an
// optional operator-selected skip-verify mode).
func httpProbeBase(ctx context.Context, cfg Config, defaultPort int) (*http.Client, string) {
	return httpProbeBaseWithTLSMode(ctx, cfg, defaultPort, NormalizeTLS(cfg.TLS))
}

// httpProbeBaseWithTLSMode builds an HTTP probe target with an explicit TLS
// policy. Most protocols use httpProbeBase and inherit cfg.TLS; protocols with
// a documented policy (such as UniFi's self-signed HTTPS default) pass their
// resolved mode here without duplicating host/port or interface binding.
func httpProbeBaseWithTLSMode(ctx context.Context, cfg Config, defaultPort int, tlsMode string) (*http.Client, string) {
	target := probeTargetFor(ctx, cfg, defaultPort)
	host, _ := target.hostPort()
	scheme := schemeHTTP
	client := httpProbeClient(target.cfg.Interface, nil)
	if tlsMode != "" {
		scheme = schemeHTTPS
		tlsConfig := tlsClientConfig(host)
		if tlsMode == tlsSkipVerify {
			tlsConfig.InsecureSkipVerify = true // operator chose tls: skip-verify
		}
		client = httpProbeClient(target.cfg.Interface, tlsConfig)
	}
	return client, scheme + urlSchemeSeparator + target.address()
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return httpProbeResponse{status: resp.StatusCode, header: resp.Header, body: body}, nil
}

// getHTTPProbe builds a plain GET for url and performs doHTTPProbe.
func getHTTPProbe(ctx context.Context, client *http.Client, url string, limit int64) (httpProbeResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		//nolint:wrapcheck // see doHTTPProbe: the caller's probeErr supplies the protocol context.
		return httpProbeResponse{}, err
	}
	return doHTTPProbe(client, req, limit)
}

// decodedJSON reports whether data parses as JSON into out. Probes use it to
// decide between a recognised API reply and a fallback endpoint, where a parse
// failure is a routing signal rather than an error.
func decodedJSON(data []byte, out any) bool {
	return json.Unmarshal(data, out) == nil
}
