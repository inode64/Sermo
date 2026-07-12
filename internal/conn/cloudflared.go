package conn

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

func init() { Register(cloudflaredProtocol{}, protocolAliasCloudflareTunnel) }

// cloudflaredProtocol probes a Cloudflare Tunnel daemon through its local
// Prometheus metrics endpoint. The endpoint is exposed by cloudflared's
// --metrics option and is commonly bound to 127.0.0.1:60123.
type cloudflaredProtocol struct{}

func (cloudflaredProtocol) Name() string       { return ProtocolNameCloudflared }
func (cloudflaredProtocol) DefaultPort() int   { return defaultPortCloudflared }
func (cloudflaredProtocol) RequiresUser() bool { return false }

const (
	cloudflaredMetricPrefix    = "cloudflared_"
	cloudflaredMetricsEndpoint = "/metrics"
)

func (cloudflaredProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, base := cloudflaredClient(cfg)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+cloudflaredMetricsEndpoint, nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPProbeLargeBody))
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("cloudflared: %s HTTP status %d", cloudflaredMetricsEndpoint, resp.StatusCode)
	}
	if !bytes.Contains(body, []byte(cloudflaredMetricPrefix)) {
		return Result{}, fmt.Errorf("cloudflared: %s response did not contain cloudflared metrics", cloudflaredMetricsEndpoint)
	}

	extra := map[string]string{
		extraEndpoint:  cloudflaredMetricsEndpoint,
		ExtraKeyStatus: strconv.Itoa(resp.StatusCode),
	}
	if ct := resp.Header.Get(httpHeaderContentType); ct != "" {
		extra[extraContentType] = ct
	}
	return Result{Extra: extra}, nil
}

func cloudflaredClient(cfg Config) (*http.Client, string) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortCloudflared
	}
	scheme := schemeHTTP
	client := httpProbeClient(cfg.Interface, nil)
	mode := normalizeTLS(cfg.TLS)
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
