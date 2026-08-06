package conn

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strconv"
)

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
	resp, err := getHTTPProbe(ctx, client, base+cloudflaredMetricsEndpoint, maxHTTPProbeLargeBody)
	if err != nil {
		return Result{}, err
	}
	if resp.status != http.StatusOK {
		return Result{}, fmt.Errorf("cloudflared: %s HTTP status %d", cloudflaredMetricsEndpoint, resp.status)
	}
	if !bytes.Contains(resp.body, []byte(cloudflaredMetricPrefix)) {
		return Result{}, fmt.Errorf("cloudflared: %s response did not contain cloudflared metrics", cloudflaredMetricsEndpoint)
	}

	extra := map[string]string{
		extraEndpoint:  cloudflaredMetricsEndpoint,
		ExtraKeyStatus: strconv.Itoa(resp.status),
	}
	if ct := resp.header.Get(httpHeaderContentType); ct != "" {
		extra[extraContentType] = ct
	}
	return Result{Extra: extra}, nil
}

func cloudflaredClient(cfg Config) (*http.Client, string) {
	return httpProbeBase(cfg, defaultPortCloudflared)
}
