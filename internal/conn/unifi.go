package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func init() { Register(unifiProtocol{}, protocolAliasUniFiController, protocolAliasUniFiNetwork) }

const (
	unifiRCOK           = "ok"
	unifiStatusEndpoint = "/status"
)

// unifiProtocol probes a UniFi Network controller (Ubiquiti) via its management
// API. It GETs the unauthenticated /status endpoint over HTTPS and verifies a
// JSON `meta.rc == "ok"` reply — proof the controller is up — reporting the
// `server_version`. The controller is HTTPS-only (default port 8443) and ships a
// self-signed certificate, so verification is skipped by default; set `tls: true`
// to require a valid certificate. No user is required (the status endpoint is
// unauthenticated).
type unifiProtocol struct{}

func (unifiProtocol) Name() string       { return ProtocolNameUniFi }
func (unifiProtocol) DefaultPort() int   { return defaultPortUniFi }
func (unifiProtocol) RequiresUser() bool { return false }

func (unifiProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPortUniFi
	}

	tc := tlsClientConfig(host)
	// UniFi controllers ship a self-signed certificate; skip verification unless
	// the operator explicitly opts into it with tls: true.
	if normalizeTLS(cfg.TLS) != ParamValueTrue {
		tc.InsecureSkipVerify = true //nolint:gosec // UniFi ships a self-signed cert; operator opts into verification with tls: true
	}
	client := httpProbeClient(cfg.Interface, tc)

	url := schemeHTTPS + urlSchemeSeparator + hostPort(host, port) + unifiStatusEndpoint
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
		return Result{}, fmt.Errorf("unifi: HTTP status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPProbeBody))

	var status struct {
		Meta struct {
			RC            string `json:"rc"`
			ServerVersion string `json:"server_version"`
			UUID          string `json:"uuid"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(body, &status); err != nil {
		return Result{}, fmt.Errorf("unifi: invalid JSON response: %w", err)
	}
	if status.Meta.RC != unifiRCOK {
		return Result{}, fmt.Errorf("unifi: status rc %q, want %s", status.Meta.RC, unifiRCOK)
	}

	extra := map[string]string{extraRC: status.Meta.RC}
	if status.Meta.UUID != "" {
		extra[extraUUID] = status.Meta.UUID
	}
	if v := status.Meta.ServerVersion; v != "" {
		extra[extraServerVer] = v
		return Result{Version: v, Extra: extra}, nil
	}
	return Result{Extra: extra}, nil
}
