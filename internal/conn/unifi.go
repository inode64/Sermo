package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"sermo/internal/netutil"
)

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
	// UniFi controllers ship a self-signed certificate; skip verification unless
	// the operator explicitly opts into it with tls: true.
	tlsMode := tlsSkipVerify
	if netutil.NormalizeTLS(cfg.TLS) == ParamValueTrue {
		tlsMode = ParamValueTrue
	}
	client, base := httpProbeBaseWithTLSMode(ctx, cfg, defaultPortUniFi, tlsMode)
	url := base + unifiStatusEndpoint
	resp, err := getHTTPProbe(ctx, client, url, maxHTTPProbeBody)
	if err != nil {
		return Result{}, err
	}
	if resp.status != http.StatusOK {
		return Result{}, fmt.Errorf("unifi: HTTP status %d", resp.status)
	}

	var status struct {
		Meta struct {
			RC            string `json:"rc"`
			ServerVersion string `json:"server_version"`
			UUID          string `json:"uuid"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(resp.body, &status); err != nil {
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
