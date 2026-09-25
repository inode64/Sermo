package conn

import (
	"context"
	"fmt"
	"net/http"
)

// prometheusProtocol probes a Prometheus server via its HTTP API. It GETs
// /api/v1/status/buildinfo and verifies a `success` status — reporting the server
// version — falling back to /-/healthy (liveness only) on older servers or when
// the endpoint is unavailable. Default port 9090. `tls` selects https; an optional
// user/password is sent as HTTP Basic auth (for a reverse proxy in front of the
// API).
type prometheusProtocol struct{}

func (prometheusProtocol) Name() string       { return ProtocolNamePrometheus }
func (prometheusProtocol) DefaultPort() int   { return defaultPortPrometheus }
func (prometheusProtocol) RequiresUser() bool { return false }

const (
	promBuildInfoEndpoint = "/api/v1/status/buildinfo"
	promHealthyEndpoint   = "/-/healthy"
	promStatusSuccess     = "success"
)

func (prometheusProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, base := httpProbeBase(cfg, defaultPortPrometheus)
	decorate := func(req *http.Request) {
		if cfg.User != "" {
			req.SetBasicAuth(cfg.User, cfg.Password)
		}
	}
	// buildinfo carries the version and proves the API is up; on a non-API reply
	// (older server, disabled endpoint) fall back to the health endpoint.
	if res, handled, err := promBuildInfo(ctx, client, base, decorate); handled {
		return res, err
	}
	return promHealthy(ctx, client, base, decorate)
}

// promBuildInfo queries /api/v1/status/buildinfo. handled is true when the result
// is conclusive (a transport error, or a recognised Prometheus API reply); it is
// false only when the endpoint is missing/not Prometheus, signalling a /-/healthy
// fallback.
func promBuildInfo(ctx context.Context, client *http.Client, base string, decorate func(*http.Request)) (res Result, handled bool, err error) {
	var info struct {
		Status string `json:"status"`
		Data   struct {
			Version  string `json:"version"`
			Revision string `json:"revision"`
		} `json:"data"`
	}
	resp, err := getHTTPProbe(ctx, client, base+promBuildInfoEndpoint, maxHTTPProbeBody, decorate)
	if err != nil {
		return Result{}, true, err // server unreachable — conclusive
	}
	if !decodedJSON(resp.body, &info) || info.Status == "" {
		return Result{}, false, nil // not the Prometheus API JSON — fall back
	}
	if resp.status != http.StatusOK {
		return Result{}, true, fmt.Errorf("prometheus buildinfo HTTP status %d", resp.status)
	}
	if info.Status != promStatusSuccess {
		return Result{}, true, fmt.Errorf("prometheus buildinfo status %q", info.Status)
	}
	extra := map[string]string{}
	if info.Data.Version != "" {
		extra[ExtraKeyVersionString] = info.Data.Version
	}
	if info.Data.Revision != "" {
		extra[extraRevision] = info.Data.Revision
	}
	return Result{Version: info.Data.Version, Extra: extra}, true, nil
}

// promHealthy queries /-/healthy, the always-available liveness endpoint.
func promHealthy(ctx context.Context, client *http.Client, base string, decorate func(*http.Request)) (Result, error) {
	resp, err := getHTTPProbe(ctx, client, base+promHealthyEndpoint, maxHTTPProbeShortBody, decorate)
	if err != nil {
		return Result{}, err
	}
	if resp.status != http.StatusOK {
		return Result{}, fmt.Errorf("prometheus: %s HTTP status %d", promHealthyEndpoint, resp.status)
	}
	return Result{}, nil
}
