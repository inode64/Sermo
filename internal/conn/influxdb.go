package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
)

func init() { Register(influxdbProtocol{}, "influx") }

// influxdbProtocol probes an InfluxDB server via its HTTP API. It GETs /health
// (InfluxDB 2.x and 1.8+) and verifies a JSON `status` of "pass" — reporting the
// server `version` — falling back to /ping (all versions), which answers 204 with
// the version in the `X-Influxdb-Version` header. Default port 8086. `tls` selects
// https (the API is plain HTTP by default). No auth — the health/ping endpoints
// are unauthenticated.
type influxdbProtocol struct{}

func (influxdbProtocol) Name() string       { return "influxdb" }
func (influxdbProtocol) DefaultPort() int   { return 8086 }
func (influxdbProtocol) RequiresUser() bool { return false }

func (influxdbProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, base := InfluxClient(cfg)

	// /health (v2 / v1.8+) carries a status and version; when the endpoint is
	// absent (older v1) the request is not "handled" and we fall back to /ping.
	if res, handled, err := influxHealth(ctx, client, base); handled {
		return res, err
	}
	return influxPing(ctx, client, base)
}

// InfluxClient builds an HTTP client and base URL for an InfluxDB server from cfg
// (host/port/tls — https when tls is set). Exported so the influxdb-query check
// reuses the same transport and addressing as the connection check.
func InfluxClient(cfg Config) (*http.Client, string) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 8086
	}
	scheme := schemeHTTP
	client := httpProbeClient(cfg.Interface, nil)
	mode := normalizeTLS(cfg.TLS)
	if mode != "" {
		scheme = schemeHTTPS
		tlsConfig := tlsClientConfig(host)
		if mode == tlsSkipVerify {
			tlsConfig.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		client = httpProbeClient(cfg.Interface, tlsConfig)
	}
	return client, scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))
}

// influxHealth queries /health. handled is true when the result is conclusive (a
// transport error, or a recognised InfluxDB health JSON); it is false only when
// the endpoint is missing/not InfluxDB, signalling a /ping fallback.
func influxHealth(ctx context.Context, client *http.Client, base string) (res Result, handled bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/health", nil)
	if err != nil {
		return Result{}, true, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, true, err // server unreachable — conclusive
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	var h struct {
		Name    string `json:"name"`
		Status  string `json:"status"`
		Version string `json:"version"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &h) != nil || h.Status == "" {
		return Result{}, false, nil // not the InfluxDB health JSON — fall back to /ping
	}
	extra := map[string]string{"status": h.Status}
	if h.Version != "" {
		extra["version_string"] = h.Version
	}
	if h.Status != "pass" {
		return Result{}, true, fmt.Errorf("influxdb health status %q: %s", h.Status, h.Message)
	}
	return Result{Version: h.Version, Extra: extra}, true, nil
}

// influxPing queries /ping, the universal liveness endpoint; the version is in
// the X-Influxdb-Version response header.
func influxPing(ctx context.Context, client *http.Client, base string) (Result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/ping", nil)
	if err != nil {
		return Result{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("influxdb: /ping HTTP status %d", resp.StatusCode)
	}
	version := resp.Header.Get("X-Influxdb-Version")
	extra := map[string]string{}
	if version != "" {
		extra["version_string"] = version
	}
	return Result{Version: version, Extra: extra}, nil
}
