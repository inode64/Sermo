package conn

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func init() { Register(rspamdProtocol{}) }

// rspamdProtocol probes an rspamd worker natively: it sends a GET /ping over
// HTTP and verifies the worker answers 200 with a "pong" body — the same
// unauthenticated liveness endpoint every rspamd worker (controller, normal,
// proxy) exposes. The default port targets the controller (11334). No auth. The
// rspamd version is read from the "Server: Rspamd/<version>" response header.
type rspamdProtocol struct{}

func (rspamdProtocol) Name() string       { return ProtocolNameRspamd }
func (rspamdProtocol) DefaultPort() int   { return defaultPortRspamd }
func (rspamdProtocol) RequiresUser() bool { return false }

const (
	rspamdPingEndpoint  = "/ping"
	rspamdVersionDelims = " \t;,"
	rspamdVersionPrefix = "rspamd/"
)

func (rspamdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortRspamd
	}
	scheme := schemeHTTP
	client := httpProbeClient(cfg.Interface, nil)
	if mode := normalizeTLS(cfg.TLS); mode != "" {
		scheme = schemeHTTPS
		tlsConfig := tlsClientConfig(host)
		if mode == tlsSkipVerify {
			tlsConfig.InsecureSkipVerify = true // operator chose tls: skip-verify
		}
		client = httpProbeClient(cfg.Interface, tlsConfig)
	}

	url := scheme + urlSchemeSeparator + hostPort(host, port) + rspamdPingEndpoint
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return Result{}, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("rspamd: HTTP status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPProbeShortBody))
	if !strings.EqualFold(strings.TrimSpace(string(body)), respPong) {
		return Result{}, fmt.Errorf("rspamd: %s returned %q, want pong", rspamdPingEndpoint, strings.TrimSpace(string(body)))
	}

	server := resp.Header.Get(httpHeaderServer)
	extra := map[string]string{extraPing: respPong}
	if server != "" {
		extra[ExtraKeyServer] = server
	}
	return Result{Version: rspamdVersion(server), Extra: extra}, nil
}

// rspamdVersion extracts the version from an rspamd "Server" header, e.g.
// "Rspamd/3.8.4" -> "3.8.4". It returns "" when the header is absent or not in
// the expected form.
func rspamdVersion(server string) string {
	i := strings.Index(strings.ToLower(server), rspamdVersionPrefix)
	if i < 0 {
		return ""
	}
	v := server[i+len(rspamdVersionPrefix):]
	if j := strings.IndexAny(v, rspamdVersionDelims); j >= 0 {
		v = v[:j]
	}
	return v
}
