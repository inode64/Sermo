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

func init() { Register(syncthingProtocol{}) }

// syncthingProtocol probes a Syncthing instance via its REST API. It GETs the
// unauthenticated /rest/noauth/health endpoint and verifies a {"status":"OK"}
// reply — proof the daemon is up. When an API key is supplied (in the password
// field, sent as X-API-Key) it also reads /rest/system/version and reports the
// Syncthing version. No user is required.
type syncthingProtocol struct{}

func (syncthingProtocol) Name() string       { return "syncthing" }
func (syncthingProtocol) DefaultPort() int   { return 8384 }
func (syncthingProtocol) RequiresUser() bool { return false }

func (syncthingProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 8384
	}
	scheme := "http"
	client := &http.Client{}
	if mode := normalizeTLS(cfg.TLS); mode != "" {
		scheme = "https"
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if mode == "skip-verify" {
			tc.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		tr.TLSClientConfig = tc
		client.Transport = tr
	}
	base := scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))

	// 1. Unauthenticated health check — proves the daemon is up.
	var health struct {
		Status string `json:"status"`
	}
	if err := syncthingGet(ctx, client, base+"/rest/noauth/health", "", &health); err != nil {
		return Result{}, err
	}
	if health.Status != "OK" {
		return Result{}, fmt.Errorf("syncthing: health status %q, want OK", health.Status)
	}

	extra := map[string]string{"health": health.Status}

	// 2. With an API key, read the version too (a bad key surfaces as an error).
	if cfg.Password != "" {
		var ver struct {
			Version string `json:"version"`
			OS      string `json:"os"`
			Arch    string `json:"arch"`
		}
		if err := syncthingGet(ctx, client, base+"/rest/system/version", cfg.Password, &ver); err != nil {
			return Result{}, err
		}
		if ver.Version != "" {
			extra["version"] = ver.Version
		}
		if ver.OS != "" {
			extra["os"] = ver.OS
		}
		if ver.Arch != "" {
			extra["arch"] = ver.Arch
		}
		return Result{Version: ver.Version, Extra: extra}, nil
	}
	return Result{Extra: extra}, nil
}

// syncthingGet performs a GET, optionally with an X-API-Key header, and decodes
// the JSON body into out. A non-200 status is an error.
func syncthingGet(ctx context.Context, client *http.Client, url, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("syncthing: HTTP status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("syncthing: invalid JSON response: %w", err)
	}
	return nil
}
