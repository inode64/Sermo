// Package dockerctl provides Docker Engine primitives for checks and service control.
package dockerctl

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
)

// defaultTimeout bounds a Docker API request when the caller's context carries
// no deadline. The Docker http.Client has no Timeout of its own (so a bounded
// caller context governs each request, and a slow stop with t=-1 is not cut
// short), so without this fallback a hung daemon that accepts the connection but
// never responds could block a watch cycle indefinitely.
const defaultTimeout = 10 * time.Second

// tlsModeSkipVerify is the tls: mode that dials without verifying the daemon's
// certificate chain.
const tlsModeSkipVerify = "skip-verify"

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
	// DefaultSocket is Docker's local Unix API socket on modern Linux systems.
	DefaultSocket = "/run/docker.sock"
	// DefaultPort is Docker's plaintext TCP API port.
	DefaultPort = 2375
)

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
		return Spec{}, true, fmt.Errorf("control must be a mapping")
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
		return Spec{}, true, fmt.Errorf("control.interface is not supported for docker control")
	}
	if spec.Socket != "" && spec.Host != "" {
		return Spec{}, true, fmt.Errorf("control must not set both socket and host")
	}
	if spec.Socket != "" && !filepath.IsAbs(spec.Socket) {
		return Spec{}, true, fmt.Errorf("control.socket %q must be an absolute path", spec.Socket)
	}
	if spec.Host != "" && strings.TrimSpace(spec.Host) == "" {
		return Spec{}, true, fmt.Errorf("control.host must not be blank")
	}
	if !ValidTLSValue(m[ControlKeyTLS]) {
		return Spec{}, true, fmt.Errorf("control.tls %q is not a valid docker TLS mode", cfgval.String(m[ControlKeyTLS]))
	}
	if spec.Host == "" && spec.Socket == "" {
		spec.Socket = DefaultSocket
	}
	if _, present := m[ControlKeyPort]; present {
		p, ok := cfgval.Int(m[ControlKeyPort])
		if !ok || p < 1 || p > 65535 {
			return Spec{}, true, fmt.Errorf("control.port must be an integer in 1..65535")
		}
		spec.Port = p
	}
	if spec.Port == 0 {
		spec.Port = DefaultPort
	}
	if spec.Container == "" {
		return Spec{}, true, fmt.Errorf("control.container is required for docker")
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
				return (&net.Dialer{}).DialContext(ctx, "unix", socket)
			},
		}
		return &Client{HTTP: &http.Client{Transport: tr}, Base: "http://docker"}, nil
	}
	host := spec.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := spec.Port
	if port == 0 {
		port = DefaultPort
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	if spec.DialContext != nil {
		tr.DialContext = spec.DialContext
	}
	scheme := "http"
	if mode := NormalizeTLS(spec.TLS); mode != "" {
		scheme = "https"
		tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if mode == tlsModeSkipVerify {
			tc.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		tr.TLSClientConfig = tc
	}
	return &Client{HTTP: &http.Client{Transport: tr}, Base: scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port))}, nil
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
		return "none"
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
	if err := c.get(ctx, "/info", &info); err != nil {
		return Info{}, err
	}
	return info, nil
}

// Inspect reads one container by name or ID.
func (c *Client) Inspect(ctx context.Context, container string) (Container, error) {
	var out Container
	if err := c.get(ctx, containerPath(container, "/json"), &out); err != nil {
		return Container{}, err
	}
	return out, nil
}

// ListContainers lists Docker containers. With all=true it includes stopped
// containers so the wizard can create a service that may be started later.
func (c *Client) ListContainers(ctx context.Context, all bool) ([]ContainerSummary, error) {
	path := "/containers/json"
	if all {
		path += "?all=1"
	}
	var out []ContainerSummary
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// Start starts a stopped container. Already-running is treated as success.
func (c *Client) Start(ctx context.Context, container string) error {
	return c.post(ctx, containerPath(container, "/start"), nil, http.StatusNoContent, http.StatusNotModified)
}

// Stop asks Docker to stop a container without delegating SIGKILL escalation to
// Docker. `t=-1` waits indefinitely after SIGTERM; Sermo's operation context is
// the outer bound and residual handling remains in the operation engine.
func (c *Client) Stop(ctx context.Context, container string) error {
	return c.post(ctx, containerPath(container, "/stop")+"?t=-1", nil, http.StatusNoContent, http.StatusNotModified)
}

// Unpause resumes a paused container. Already-unpaused is treated as success.
func (c *Client) Unpause(ctx context.Context, container string) error {
	return c.post(ctx, containerPath(container, "/unpause"), nil, http.StatusNoContent, http.StatusNotModified)
}

func (c *Client) get(ctx context.Context, path string, out any) error {
	ctx, cancel := ensureDeadline(ctx)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return dockerStatusError(resp)
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
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
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	for _, code := range ok {
		if resp.StatusCode == code {
			return nil
		}
	}
	return dockerStatusError(resp)
}

func dockerStatusError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	return fmt.Errorf("docker: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

func containerPath(container, suffix string) string {
	return "/containers/" + url.PathEscape(container) + suffix
}

// NormalizeTLS maps friendly TLS values to Docker HTTP client modes.
func NormalizeTLS(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "false", "no", "off":
		return ""
	case "true", "yes", "on", "required":
		return "true"
	case tlsModeSkipVerify:
		return tlsModeSkipVerify
	default:
		return s
	}
}

// ValidTLSValue reports whether v is accepted by Sermo's Docker transport.
func ValidTLSValue(v any) bool {
	switch t := v.(type) {
	case nil:
		return true
	case bool:
		return true
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "false", "yes", "no", "on", "off", "required", tlsModeSkipVerify:
			return true
		default:
			return false
		}
	default:
		return false
	}
}
