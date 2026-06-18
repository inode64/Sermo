package conn

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
)

func init() { Register(cloudflaredProtocol{}, "cloudflare-tunnel") }

// cloudflaredProtocol probes a Cloudflare Tunnel daemon through its local
// Prometheus metrics endpoint. The endpoint is exposed by cloudflared's
// --metrics option and is commonly bound to 127.0.0.1:60123.
type cloudflaredProtocol struct{}

func (cloudflaredProtocol) Name() string       { return "cloudflared" }
func (cloudflaredProtocol) DefaultPort() int   { return 60123 }
func (cloudflaredProtocol) RequiresUser() bool { return false }

func (cloudflaredProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, base := cloudflaredClient(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/metrics", nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("cloudflared: /metrics HTTP status %d", resp.StatusCode)
	}
	if !bytes.Contains(body, []byte("cloudflared_")) {
		return Result{}, fmt.Errorf("cloudflared: /metrics response did not contain cloudflared metrics")
	}

	extra := map[string]string{
		"endpoint": "/metrics",
		"status":   strconv.Itoa(resp.StatusCode),
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		extra["content_type"] = ct
	}
	return Result{Extra: extra}, nil
}

func cloudflaredClient(cfg Config) (*http.Client, string) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 60123
	}
	scheme := "http"
	client := httpProbeClient(cfg.Interface, nil)
	mode := normalizeTLS(cfg.TLS)
	if mode != "" {
		scheme = "https"
		tlsConfig := tlsClientConfig(host)
		if mode == "skip-verify" {
			tlsConfig.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		client = httpProbeClient(cfg.Interface, tlsConfig)
	}
	return client, scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))
}
