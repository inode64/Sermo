package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

func init() { Register(syncthingProtocol{}) }

const (
	syncthingHealthEndpoint  = "/rest/noauth/health"
	syncthingHealthOK        = "OK"
	syncthingVersionEndpoint = "/rest/system/version"
)

// syncthingProtocol probes a Syncthing instance via its REST API. It GETs the
// unauthenticated /rest/noauth/health endpoint and verifies a {"status":"OK"}
// reply — proof the daemon is up. When an API key is supplied (in the password
// field, sent as X-API-Key) it also reads /rest/system/version and reports the
// Syncthing version. No user is required.
type syncthingProtocol struct{}

func (syncthingProtocol) Name() string       { return ProtocolNameSyncthing }
func (syncthingProtocol) DefaultPort() int   { return defaultPortSyncthing }
func (syncthingProtocol) RequiresUser() bool { return false }

func (syncthingProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, base := httpProbeBase(cfg, defaultPortSyncthing)

	// 1. Unauthenticated health check — proves the daemon is up.
	var health struct {
		Status string `json:"status"`
	}
	if err := syncthingGet(ctx, client, base+syncthingHealthEndpoint, "", &health); err != nil {
		return Result{}, err
	}
	if health.Status != syncthingHealthOK {
		return Result{}, fmt.Errorf("syncthing: health status %q, want OK", health.Status)
	}

	extra := map[string]string{extraHealth: health.Status}

	// 2. With an API key, read the version too (a bad key surfaces as an error).
	if cfg.Password != "" {
		var ver struct {
			Version string `json:"version"`
			OS      string `json:"os"`
			Arch    string `json:"arch"`
		}
		if err := syncthingGet(ctx, client, base+syncthingVersionEndpoint, cfg.Password, &ver); err != nil {
			return Result{}, err
		}
		if ver.Version != "" {
			extra[extraVersion] = ver.Version
		}
		if ver.OS != "" {
			extra[extraOS] = ver.OS
		}
		if ver.Arch != "" {
			extra[extraArch] = ver.Arch
		}
		return Result{Version: ver.Version, Extra: extra}, nil
	}
	return Result{Extra: extra}, nil
}

// syncthingGet performs a GET, optionally with an X-API-Key header, and decodes
// the JSON body into out. A non-200 status is an error.
func syncthingGet(ctx context.Context, client *http.Client, url, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return err
	}
	if apiKey != "" {
		req.Header.Set(httpHeaderSyncthingAuth, apiKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("syncthing: HTTP status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPProbeBody))
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("syncthing: invalid JSON response: %w", err)
	}
	return nil
}
