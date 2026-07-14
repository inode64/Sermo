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
