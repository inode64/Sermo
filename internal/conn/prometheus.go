package conn

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
)

func init() { Register(prometheusProtocol{}, "prom") }

// prometheusProtocol probes a Prometheus server via its HTTP API. It GETs
// /api/v1/status/buildinfo and verifies a `success` status — reporting the server
// version — falling back to /-/healthy (liveness only) on older servers or when
// the endpoint is unavailable. Default port 9090. `tls` selects https; an optional
// user/password is sent as HTTP Basic auth (for a reverse proxy in front of the
// API).
type prometheusProtocol struct{}

func (prometheusProtocol) Name() string       { return "prometheus" }
func (prometheusProtocol) DefaultPort() int   { return 9090 }
func (prometheusProtocol) RequiresUser() bool { return false }

func (prometheusProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, base := PrometheusClient(cfg)
	// buildinfo carries the version and proves the API is up; on a non-API reply
	// (older server, disabled endpoint) fall back to the health endpoint.
	if res, handled, err := promBuildInfo(ctx, client, base, cfg); handled {
		return res, err
	}
	return promHealthy(ctx, client, base, cfg)
}

// PrometheusClient builds an HTTP client and base URL for a Prometheus server
// from cfg (host/port/tls — https when tls is set). Exported so a PromQL query
// check can reuse the same transport and addressing.
func PrometheusClient(cfg Config) (*http.Client, string) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 9090
	}
	scheme := "http"
	client := &http.Client{}
	mode := normalizeTLS(cfg.TLS)
	if cfg.Interface != "" || mode != "" {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		if cfg.Interface != "" {
			tr.DialContext = BindDialer(cfg.Interface).DialContext
		}
		if mode != "" {
			scheme = "https"
			tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
			if mode == "skip-verify" {
				tc.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
			}
			tr.TLSClientConfig = tc
		}
		client.Transport = tr
	}
	return client, scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// promBuildInfo queries /api/v1/status/buildinfo. handled is true when the result
// is conclusive (a transport error, or a recognised Prometheus API reply); it is
// false only when the endpoint is missing/not Prometheus, signalling a /-/healthy
// fallback.
func promBuildInfo(ctx context.Context, client *http.Client, base string, cfg Config) (res Result, handled bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/status/buildinfo", nil)
	if err != nil {
		return Result{}, true, err
	}
	promAuth(req, cfg)
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, true, err // server unreachable — conclusive
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var info struct {
		Status string `json:"status"`
		Data   struct {
			Version  string `json:"version"`
			Revision string `json:"revision"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &info) != nil || info.Status == "" {
		return Result{}, false, nil // not the Prometheus API JSON — fall back
	}
	if info.Status != "success" {
		return Result{}, true, fmt.Errorf("prometheus buildinfo status %q", info.Status)
	}
	extra := map[string]string{}
	if info.Data.Version != "" {
		extra["version_string"] = info.Data.Version
	}
	if info.Data.Revision != "" {
		extra["revision"] = info.Data.Revision
	}
	return Result{Version: info.Data.Version, Extra: extra}, true, nil
}

// promHealthy queries /-/healthy, the always-available liveness endpoint.
func promHealthy(ctx context.Context, client *http.Client, base string, cfg Config) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/-/healthy", nil)
	if err != nil {
		return Result{}, err
	}
	promAuth(req, cfg)
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("prometheus: /-/healthy HTTP status %d", resp.StatusCode)
	}
	return Result{}, nil
}

// promAuth adds HTTP Basic auth when a user is configured (for a reverse proxy).
func promAuth(req *http.Request, cfg Config) {
	if cfg.User != "" {
		req.SetBasicAuth(cfg.User, cfg.Password)
	}
}
