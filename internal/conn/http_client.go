package conn

import (
	"crypto/tls"
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
func httpProbeBase(cfg Config, defaultPort int) (*http.Client, string) {
	host, port := cfg.hostPortDefaults(defaultPort)
	scheme := schemeHTTP
	client := httpProbeClient(cfg.Interface, nil)
	mode := NormalizeTLS(cfg.TLS)
	if mode != "" {
		scheme = schemeHTTPS
		tlsConfig := tlsClientConfig(host)
		if mode == tlsSkipVerify {
			tlsConfig.InsecureSkipVerify = true // operator chose tls: skip-verify
		}
		client = httpProbeClient(cfg.Interface, tlsConfig)
	}
	return client, scheme + urlSchemeSeparator + hostPort(host, port)
}
