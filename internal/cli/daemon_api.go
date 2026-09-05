package cli

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"sermo/internal/hostfs"
	"strings"

	"sermo/internal/config"
	"sermo/internal/httpx"
	"sermo/internal/netutil"
	"sermo/internal/web"
)

// pruneDaemonEvents performs the HTTP call to the running sermod's web API
// to prune its event log. It reads the web: address/port and any
// admin password from the shared config so local sermoctl can authenticate
// the same way the operator would via the UI.
// daemonWebRequest loads config through the operator-facing path, then delegates
// the HTTP exchange to daemonWebDo. The caller owns the response body and its
// own status/decode handling.
func (a App) daemonWebRequest(ctx context.Context, opts options, method, what string, csrf bool, buildURL func(base string) string) (*http.Response, error) {
	cfg, code := a.loadConfig(opts)
	if code != exitSuccess || cfg == nil {
		return nil, errors.New("failed to load config")
	}
	return a.daemonWebDo(ctx, cfg, method, what, csrf, buildURL)
}

// daemonWebDo is the transport owner for requests from sermoctl to sermod's web
// API. It resolves the endpoint, builds and authenticates the request, attaches
// mutation headers, and performs the bounded exchange. The caller owns the
// response body.
func (a App) daemonWebDo(ctx context.Context, cfg *config.Config, method, what string, csrf bool, buildURL func(base string) string) (*http.Response, error) {
	base, err := webAPIBase(cfg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, buildURL(base), http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build %s request: %w", what, err)
	}
	if csrf {
		req.Header.Set(web.HeaderCSRF, web.CSRFHeaderValue)
		// A mutation must name the generation it was aimed at, so a reload cannot
		// swap the target's identity underneath it. Read it first: without the
		// header the daemon answers 428 and the mutation never runs.
		if generation := a.daemonWebGeneration(ctx, cfg); generation != "" {
			req.Header.Set(web.HeaderGeneration, generation)
		}
	}
	// If the config declares an admin password, send Basic auth (any user + pw).
	a.applyDaemonWebAuth(req, cfg)

	client := httpx.NewClient(httpx.ClientOptions{Timeout: daemonWebClientTimeout})
	resp, err := httpx.Do(client, req)
	if err != nil {
		return nil, fmt.Errorf("talking to daemon web UI: %w (is sermod running with web.port set?)", err)
	}
	return resp, nil
}

// daemonWebGeneration reads the daemon's current backend generation, which every
// response carries. An empty answer means the daemon is not tracking generations
// (or could not be reached), and the caller sends no header — which is exactly
// what the daemon expects in that case.
func (a App) daemonWebGeneration(ctx context.Context, cfg *config.Config) string {
	// csrf=false is the recursion boundary: generation lookup uses the shared
	// transport but never asks for another generation lookup itself.
	resp, err := a.daemonWebDo(ctx, cfg, http.MethodGet, "daemon generation", false, func(base string) string {
		return base + web.APIPathWatches
	})
	if err != nil {
		return ""
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	return strings.TrimSpace(resp.Header.Get(web.HeaderGeneration))
}

func (a App) applyDaemonWebAuth(req *http.Request, cfg *config.Config) {
	if pw := a.daemonWebPassword(cfg); pw != "" {
		req.Header.Set(daemonWebHeaderAuthorization, daemonWebBasicAuth(pw))
	}
}

// daemonWebPassword resolves the credential sermoctl sends to the daemon web
// API, in order of precedence:
//
//  1. $SERMO_WEB_PASSWORD — an explicit operator choice, and the only option
//     when the daemon is not on this host.
//  2. <paths.runtime>/web.token — the daemon's own runtime token, but only for a
//     daemon on this host: the token of a local sermod means nothing to a remote
//     one. It is what makes hashed credentials usable, since a hash cannot be
//     turned back into the password the API expects.
//
// An empty result means no credential was found. The request is still sent: the
// dashboard may have no authentication at all, and a 401 is reported by the
// caller with the guidance in daemonWebAuthHint.
func (a App) daemonWebPassword(cfg *config.Config) string {
	if pw := strings.TrimSpace(a.env(config.EnvWebPassword)); pw != "" {
		return pw
	}
	if daemonIsLocal(cfg) {
		if token := readDaemonWebToken(cfg.Global.RuntimeDir()); token != "" {
			return token
		}
	}
	return ""
}

// daemonIsLocal reports whether web.address names this host. A runtime token is
// only a credential for the daemon that wrote it, so sending it to a remote
// sermod would replace a working password with a guaranteed 401.
func daemonIsLocal(cfg *config.Config) bool {
	bind, err := cfg.Global.WebBind()
	if err != nil {
		return false
	}
	addr := bind.Host
	if addr == daemonWebLocalhostName || strings.HasSuffix(addr, "."+daemonWebLocalhostName) {
		return true
	}
	ip := net.ParseIP(addr)
	// A wildcard bind (0.0.0.0, ::) is reached over loopback from this host.
	return ip != nil && (ip.IsLoopback() || ip.IsUnspecified())
}

// daemonWebAuthHint tells the operator how to supply a credential when the
// daemon refuses the request. Hashed credentials cannot be sent as a password,
// so the runtime token or SERMO_WEB_PASSWORD is the way in.
const daemonWebAuthHint = " — run as the daemon user so " + config.DaemonWebTokenFilename +
	" under paths.runtime is readable, or set " + config.EnvWebPassword

// daemonWebStatusHint appends the credential hint to a refused request, and
// nothing to any other failure.
func daemonWebStatusHint(status int) string {
	if status == http.StatusUnauthorized {
		return daemonWebAuthHint
	}
	return ""
}

// readDaemonWebToken reads the daemon's runtime token, or "" when it is absent
// or unreadable (sermoctl running as another user).
func readDaemonWebToken(runtimeDir string) string {
	if runtimeDir == "" {
		runtimeDir = config.DefaultRuntime
	}
	data, err := hostfs.ReadFile(filepath.Join(runtimeDir, config.DaemonWebTokenFilename))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func daemonWebBasicAuth(password string) string {
	cred := base64.StdEncoding.EncodeToString([]byte(daemonWebAuthUserPrefix + password))
	return daemonWebBasicAuthPrefix + cred
}

// daemonAPIGet silently loads config for best-effort status enrichment, then
// performs an authenticated GET against the running sermod web API.
// daemonAPIJSON loads the config, GETs one daemon API path and decodes a 200
// body into out. Any transport, status or decode problem reports false: these
// reads only enrich CLI output and must never turn into a failure.
func (a App) daemonAPIJSON(ctx context.Context, opts options, path string, out any) bool {
	cfg, err := a.LoadConfig(opts.globalPath())
	if err != nil || cfg == nil {
		return false
	}
	return a.daemonAPIJSONWithConfig(ctx, cfg, path, out)
}

// daemonAPIJSONWithConfig is daemonAPIJSON for callers that already loaded the
// config, such as service-name canonicalization.
func (a App) daemonAPIJSONWithConfig(ctx context.Context, cfg *config.Config, path string, out any) bool {
	body, status, err := a.daemonAPIGetWithConfig(ctx, cfg, path)
	if err != nil || status != http.StatusOK {
		return false
	}
	return json.Unmarshal(body, out) == nil
}

func (a App) daemonAPIGetWithConfig(ctx context.Context, cfg *config.Config, path string) ([]byte, int, error) {
	resp, err := a.daemonWebDo(ctx, cfg, http.MethodGet, "daemon API", false, func(base string) string {
		return base + path
	})
	if err != nil {
		return nil, 0, fmt.Errorf("daemon API GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read daemon API response for %s: %w", path, err)
	}
	return body, resp.StatusCode, nil
}

// fetchDaemonServiceState reads GET /api/services/{name} from the running
// sermod web API and returns its computed state field.
func (a App) fetchDaemonServiceState(ctx context.Context, opts options, service string) (string, bool) {
	cfg, err := a.LoadConfig(opts.globalPath())
	if err != nil || cfg == nil {
		return "", false
	}
	name := service
	if canonical, ok := cfg.CanonicalServiceName(service); ok {
		name = canonical
	} else if len(cfg.Services) > 0 {
		return "", false
	}
	var detail struct {
		State string `json:"state"`
	}
	if !a.daemonAPIJSONWithConfig(ctx, cfg, web.APIPathServices+"/"+url.PathEscape(name), &detail) || detail.State == "" {
		return "", false
	}
	return detail.State, true
}

func (a App) fetchDaemonWatchDetail(ctx context.Context, opts options, watch string) (daemonWatchDetail, bool) {
	var watches []daemonWatchDetail
	if !a.daemonAPIJSON(ctx, opts, web.APIPathWatches, &watches) {
		return daemonWatchDetail{}, false
	}
	for _, detail := range watches {
		if detail.Name == watch {
			return detail, true
		}
	}
	return daemonWatchDetail{}, false
}

func (a App) fetchDaemonApplicationStates(ctx context.Context, opts options) map[string]string {
	var apps []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if !a.daemonAPIJSON(ctx, opts, web.APIPathApplications, &apps) {
		return nil
	}
	out := make(map[string]string, len(apps))
	for _, application := range apps {
		if application.Name != "" && application.State != "" {
			out[application.Name] = application.State
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func webAPIBase(cfg *config.Config) (string, error) {
	bind, err := cfg.Global.WebBind()
	if err != nil {
		switch {
		case errors.Is(err, config.ErrWebNotConfigured):
			return "", errors.New("web UI is not enabled in config (no web: block or no port); the event API is exposed by the running daemon")
		case errors.Is(err, config.ErrWebPortUnset):
			return "", errors.New("web.port is not set in config")
		default:
			// WebBind already returns an operator-facing message; wrapcheck
			// still requires wrapping the external error, so the wrap is identity.
			return "", fmt.Errorf("%w", err) // nosemgrep: error-wrap-must-add-context
		}
	}
	return daemonWebSchemeHTTP + netutil.URLSchemeSeparator + bind.HostPort(), nil
}
