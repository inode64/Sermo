package conn

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

func init() { Register(rspamdProtocol{}) }

// rspamdProtocol probes an rspamd worker natively: it sends a GET /ping over
// HTTP and verifies the worker answers 200 with a "pong" body — the same
// unauthenticated liveness endpoint every rspamd worker (controller, normal,
// proxy) exposes. The default port targets the controller (11334). No auth. The
// rspamd version is read from the "Server: Rspamd/<version>" response header.
type rspamdProtocol struct{}

func (rspamdProtocol) Name() string       { return "rspamd" }
func (rspamdProtocol) DefaultPort() int   { return 11334 }
func (rspamdProtocol) RequiresUser() bool { return false }

func (rspamdProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 11334
	}
	scheme := "http"
	client := httpProbeClient(cfg.Interface, nil)
	if mode := normalizeTLS(cfg.TLS); mode != "" {
		scheme = "https"
		tlsConfig := tlsClientConfig(host)
		if mode == tlsSkipVerify {
			tlsConfig.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		client = httpProbeClient(cfg.Interface, tlsConfig)
	}

	url := scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/ping"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if !strings.EqualFold(strings.TrimSpace(string(body)), "pong") {
		return Result{}, fmt.Errorf("rspamd: /ping returned %q, want pong", strings.TrimSpace(string(body)))
	}

	server := resp.Header.Get("Server")
	extra := map[string]string{"ping": "pong"}
	if server != "" {
		extra["server"] = server
	}
	return Result{Version: rspamdVersion(server), Extra: extra}, nil
}

// rspamdVersion extracts the version from an rspamd "Server" header, e.g.
// "Rspamd/3.8.4" -> "3.8.4". It returns "" when the header is absent or not in
// the expected form.
func rspamdVersion(server string) string {
	const prefix = "rspamd/"
	i := strings.Index(strings.ToLower(server), prefix)
	if i < 0 {
		return ""
	}
	v := server[i+len(prefix):]
	if j := strings.IndexAny(v, " \t;,"); j >= 0 {
		v = v[:j]
	}
	return v
}
