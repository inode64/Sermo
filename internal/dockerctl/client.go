// Package dockerctl provides Docker Engine primitives for checks and service control.
package dockerctl

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/httpx"
	"sermo/internal/netutil"
	"sermo/internal/units"
)

// defaultTimeout bounds a Docker API request when the caller's context carries
// no deadline. The Docker http.Client has no Timeout of its own (so a bounded
// caller context governs each request, and a slow stop with t=-1 is not cut
// short), so without this fallback a hung daemon that accepts the connection but
// never responds could block a watch cycle indefinitely.
const defaultTimeout = 10 * time.Second

const (
	dockerResponseBodyLimit = 256 * units.BytesPerKiB
	dockerErrorBodyLimit    = 4 * units.BytesPerKiB
)

const (
	dockerSchemeHTTP          = netutil.URLSchemeHTTP
	dockerSchemeHTTPS         = netutil.URLSchemeHTTPS
	dockerSocketBaseURL       = dockerSchemeHTTP + netutil.URLSchemeSeparator + "docker"
	dockerEndpointInfo        = "/info"
	dockerEndpointContainers  = "/containers/json"
	dockerEndpointInspect     = "/json"
	dockerEndpointStart       = "/start"
	dockerEndpointStop        = "/stop"
	dockerEndpointUnpause     = "/unpause"
	dockerQueryAll            = "?all=1"
	dockerQueryNoKillStop     = "?t=-1"
	dockerContainerPathPrefix = "/containers/"
)

// tlsModeSkipVerify is the canonical unverified-TLS mode; the accepted input
// spellings live in netutil (NormalizeTLS / ValidTLSValue).
const tlsModeSkipVerify = netutil.TLSModeSkipVerify

// TLSValueSummary is the compact user-facing list of Docker TLS modes.
const TLSValueSummary = "true, false, required, skip-verify"

// ensureDeadline returns ctx unchanged when it already carries a deadline,
// otherwise a child bounded by defaultTimeout. The returned cancel must be
// called.
func ensureDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, defaultTimeout)
}

const (
	// DefaultHost is Docker's loopback host when TCP control omits host.
	DefaultHost = netutil.LoopbackIPv4
	// DefaultSocket is Docker's local Unix API socket on modern Linux systems.
	DefaultSocket = "/run/docker.sock"
	// DefaultPort is Docker's plaintext TCP API port.
	DefaultPort = 2375
)

// HealthStatusNone is emitted when Docker exposes no container health state.
const HealthStatusNone = "none"

// ContainerStatus* constants are Docker `.State.Status` labels from inspect.
const (
	ContainerStatusCreated    = "created"
	ContainerStatusDead       = "dead"
	ContainerStatusExited     = "exited"
	ContainerStatusPaused     = "paused"
	ContainerStatusRemoving   = "removing"
	ContainerStatusRestarting = "restarting"
	ContainerStatusRunning    = "running"
)

const networkUnix = netutil.NetworkUnix

// ControlType is the service control.type value for Docker-backed services.
const ControlType = "docker"

const sectionControl = "control"

// ControlKey constants are keys inside a Docker service control block.
const (
	ControlKeyType      = "type"
	ControlKeySocket    = "socket"
	ControlKeyHost      = "host"
	ControlKeyPort      = "port"
	ControlKeyTLS       = "tls"
	ControlKeyContainer = "container"
	ControlKeyInterface = "interface"
)

const (
	controlPathContainer = sectionControl + "." + ControlKeyContainer
	controlPathHost      = sectionControl + "." + ControlKeyHost
	controlPathInterface = sectionControl + "." + ControlKeyInterface
	controlPathPort      = sectionControl + "." + ControlKeyPort
	controlPathSocket    = sectionControl + "." + ControlKeySocket
	controlPathTLS       = sectionControl + "." + ControlKeyTLS
)

// Spec describes a Docker Engine endpoint and the target container for control.
type Spec struct {
	Socket    string
	Host      string
	Port      int
	TLS       string
	Container string
	// DialContext overrides TCP dialing. The Docker connection check injects
	// conn.BindDialer here so SO_BINDTODEVICE stays owned by internal/conn.
	DialContext func(context.Context, string, string) (net.Conn, error)
}

// SpecFromTree reads a service's optional `control: {type: docker, ...}` block.
func SpecFromTree(tree map[string]any) (Spec, bool, error) {
	raw, present := tree[sectionControl]
	if !present {
		return Spec{}, false, nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return Spec{}, true, errors.New("control must be a mapping")
	}
	if typ := cfgval.String(m[ControlKeyType]); typ != ControlType {
		return Spec{}, false, nil
	}
	spec := Spec{
		Socket:    cfgval.String(m[ControlKeySocket]),
		Host:      cfgval.String(m[ControlKeyHost]),
		TLS:       cfgval.String(m[ControlKeyTLS]),
		Container: cfgval.String(m[ControlKeyContainer]),
	}
	if _, present := m[ControlKeyInterface]; present {
		return Spec{}, true, fmt.Errorf("%s is not supported for docker control", controlPathInterface)
	}
	if spec.Socket != "" && spec.Host != "" {
		return Spec{}, true, errors.New("control must not set both socket and host")
	}
	if spec.Socket != "" && !filepath.IsAbs(spec.Socket) {
		return Spec{}, true, fmt.Errorf("%s %q must be an absolute path", controlPathSocket, spec.Socket)
	}
	if spec.Host != "" && strings.TrimSpace(spec.Host) == "" {
		return Spec{}, true, fmt.Errorf("%s must not be blank", controlPathHost)
	}
	if !ValidTLSValue(m[ControlKeyTLS]) {
		return Spec{}, true, fmt.Errorf("%s %q is not a valid docker TLS mode", controlPathTLS, cfgval.String(m[ControlKeyTLS]))
	}
	if spec.Host == "" && spec.Socket == "" {
		spec.Socket = DefaultSocket
	}
	if _, present := m[ControlKeyPort]; present {
		p, ok := cfgval.Int(m[ControlKeyPort])
		if !ok || !cfgval.ValidTCPPort(p) {
			return Spec{}, true, fmt.Errorf("%s must be an integer in %s", controlPathPort, cfgval.TCPPortRange())
		}
		spec.Port = p
	}
	if spec.Port == 0 {
		spec.Port = DefaultPort
	}
	if spec.Container == "" {
		return Spec{}, true, fmt.Errorf("%s is required for docker", controlPathContainer)
	}
	return spec, true, nil
}

// Client talks to the Docker Engine HTTP API.
type Client struct {
	HTTP *http.Client
	Base string
}

// NewClient returns a Docker API client for spec.
func NewClient(spec Spec) (*Client, error) {
	if spec.Socket != "" {
		socket := filepath.Clean(spec.Socket)
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, networkUnix, socket)
			},
		}
		return &Client{HTTP: &http.Client{Transport: tr}, Base: dockerSocketBaseURL}, nil
	}
	host := spec.Host
	if host == "" {
		host = DefaultHost
	}
	port := spec.Port
	if port == 0 {
		port = DefaultPort
	}
	tr := httpx.CloneDefaultTransport()
	if spec.DialContext != nil {
		tr.DialContext = spec.DialContext
	}
	scheme := dockerSchemeHTTP
	if mode := NormalizeTLS(spec.TLS); mode != "" {
		scheme = dockerSchemeHTTPS
		tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if mode == tlsModeSkipVerify {
			tc.InsecureSkipVerify = true // operator chose tls: skip-verify
		}
		tr.TLSClientConfig = tc
	}
	return &Client{HTTP: &http.Client{Transport: tr}, Base: scheme + netutil.URLSchemeSeparator + netutil.JoinHostPort(host, port)}, nil
}

// CloseIdleConnections closes idle HTTP transport connections.
func (c *Client) CloseIdleConnections() {
	if c != nil && c.HTTP != nil {
		c.HTTP.CloseIdleConnections()
	}
}

// Info is the subset of Docker /info Sermo observes.
type Info struct {
	Containers        int      `json:"Containers"`
	ContainersRunning int      `json:"ContainersRunning"`
	ContainersPaused  int      `json:"ContainersPaused"`
	ContainersStopped int      `json:"ContainersStopped"`
	Images            int      `json:"Images"`
	ServerVersion     string   `json:"ServerVersion"`
	Warnings          []string `json:"Warnings"`
}

// Health is the health section of a Docker container state.
type Health struct {
	Status string `json:"Status"`
}

// ContainerState is the subset of Docker container state Sermo needs.
type ContainerState struct {
	Status     string  `json:"Status"`
	Running    bool    `json:"Running"`
	Paused     bool    `json:"Paused"`
	Restarting bool    `json:"Restarting"`
	Dead       bool    `json:"Dead"`
	Pid        int     `json:"Pid"`
	ExitCode   int     `json:"ExitCode"`
	Health     *Health `json:"Health"`
}

// Container is the subset of Docker inspect data Sermo needs.
type Container struct {
	ID           string         `json:"Id"`
	Name         string         `json:"Name"`
	RestartCount int            `json:"RestartCount"`
	State        ContainerState `json:"State"`
}

// ContainerSummary is the subset of Docker's container list Sermo needs for the
// wizard.
type ContainerSummary struct {
	ID     string   `json:"Id"`
	Names  []string `json:"Names"`
	State  string   `json:"State"`
	Status string   `json:"Status"`
}

// HealthStatus returns the stable health label for a container.
func (c Container) HealthStatus() string {
	if c.State.Health == nil || c.State.Health.Status == "" {
		return HealthStatusNone
	}
	return c.State.Health.Status
}

// ContainerName returns the Docker name without its leading slash.
func (c Container) ContainerName() string {
	return strings.TrimPrefix(c.Name, "/")
}

// Info reads Docker daemon info.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var info Info
	if err := c.get(ctx, dockerEndpointInfo, &info); err != nil {
		return Info{}, err
	}
	return info, nil
}

// Inspect reads one container by name or ID.
func (c *Client) Inspect(ctx context.Context, container string) (Container, error) {
	var out Container
	if err := c.get(ctx, containerPath(container, dockerEndpointInspect), &out); err != nil {
		return Container{}, err
	}
	return out, nil
}

// ListContainers lists Docker containers. With all=true it includes stopped
// containers so the wizard can create a service that may be started later.
func (c *Client) ListContainers(ctx context.Context, all bool) ([]ContainerSummary, error) {
	path := dockerEndpointContainers
	if all {
		path += dockerQueryAll
	}
	var out []ContainerSummary
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Start starts a stopped container. Already-running is treated as success.
func (c *Client) Start(ctx context.Context, container string) error {
	return c.post(ctx, containerPath(container, dockerEndpointStart), nil, http.StatusNoContent, http.StatusNotModified)
}

// Stop asks Docker to stop a container without delegating SIGKILL escalation to
// Docker. `t=-1` waits indefinitely after SIGTERM; Sermo's operation context is
// the outer bound and residual handling remains in the operation engine.
func (c *Client) Stop(ctx context.Context, container string) error {
	return c.post(ctx, containerPath(container, dockerEndpointStop)+dockerQueryNoKillStop, nil, http.StatusNoContent, http.StatusNotModified)
}

// Unpause resumes a paused container. Already-unpaused is treated as success.
func (c *Client) Unpause(ctx context.Context, container string) error {
	return c.post(ctx, containerPath(container, dockerEndpointUnpause), nil, http.StatusNoContent, http.StatusNotModified)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	ctx, cancel := ensureDeadline(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, http.NoBody)
	if err != nil {
		return fmt.Errorf("build Docker GET %s request: %w", path, err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("send Docker GET %s request: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return dockerStatusError(resp)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, dockerResponseBodyLimit))
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("docker: invalid JSON response: %w", err)
	}
	return nil
}

func (c *Client) post(ctx context.Context, path string, body io.Reader, ok ...int) error {
	ctx, cancel := ensureDeadline(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Base+path, body)
	if err != nil {
		return fmt.Errorf("build Docker POST %s request: %w", path, err)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("send Docker POST %s request: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if slices.Contains(ok, resp.StatusCode) {
		return nil
	}
	return dockerStatusError(resp)
}

func dockerStatusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, dockerErrorBodyLimit))
	return fmt.Errorf("docker: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func containerPath(container, suffix string) string {
	return dockerContainerPathPrefix + url.PathEscape(container) + suffix
}

// NormalizeTLS maps friendly TLS values to Docker HTTP client modes. It shares
// the conn probes' normalization: the accepted spellings and canonical modes
// are the same (netutil.NormalizeTLS).
func NormalizeTLS(s string) string {
	return netutil.NormalizeTLS(s)
}

// ValidTLSValue reports whether v is accepted by Sermo's Docker transport: an
// omitted or boolean value, or one of the shared friendly tls spellings.
func ValidTLSValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return true
	case string:
		return netutil.ValidTLSValue(t)
	default:
		return false
	}
}
