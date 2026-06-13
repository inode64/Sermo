package conn

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
)

func init() { Register(dockerProtocol{}) }

// dockerProtocol probes a Docker Engine daemon over its HTTP API, by default on
// the local Unix socket /var/run/docker.sock (set `host` for a TCP daemon, with
// `tls` for 2376). It GETs /info — proving the daemon is up — and exposes the
// container counts (total/running/paused/stopped), image count and daemon warning
// count as variables, plus the engine version. With a `container` selected it also
// reads that container's state and health. Operators alert on any of these with
// `expect` (e.g. containers.running) or on a container's state change with
// `on_change`; the engine version drives `on_version_change`.
type dockerProtocol struct{}

// Name returns the canonical type token.
func (dockerProtocol) Name() string { return "docker" }

// DefaultPort is Docker's plaintext TCP port (use 2376 with tls). Ignored when
// probing the default Unix socket.
func (dockerProtocol) DefaultPort() int { return 2375 }

// RequiresUser reports that no user is required (the socket/TCP endpoint is
// authorized by the OS / TLS client cert, not a username).
func (dockerProtocol) RequiresUser() bool { return false }

// Probe reads /info and, when a container is selected, that container's state.
func (dockerProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, base, err := dockerClient(cfg)
	if err != nil {
		return Result{}, err
	}
	defer client.CloseIdleConnections()

	var info struct {
		Containers        int      `json:"Containers"`
		ContainersRunning int      `json:"ContainersRunning"`
		ContainersPaused  int      `json:"ContainersPaused"`
		ContainersStopped int      `json:"ContainersStopped"`
		Images            int      `json:"Images"`
		ServerVersion     string   `json:"ServerVersion"`
		Warnings          []string `json:"Warnings"`
	}
	if err := dockerGet(ctx, client, base+"/info", &info); err != nil {
		return Result{}, err
	}
	res := Result{Version: info.ServerVersion, Extra: map[string]string{
		"containers":         strconv.Itoa(info.Containers),
		"containers.running": strconv.Itoa(info.ContainersRunning),
		"containers.paused":  strconv.Itoa(info.ContainersPaused),
		"containers.stopped": strconv.Itoa(info.ContainersStopped),
		"images":             strconv.Itoa(info.Images),
		"warnings":           strconv.Itoa(len(info.Warnings)),
	}}

	if name := cfg.Query; name != "" {
		if err := dockerContainer(ctx, client, base, name, &res); err != nil {
			return Result{}, err
		}
	}
	return res, nil
}

// dockerContainer reads one container's state/health into res.Extra and sets the
// change fingerprint so on_change tracks its state/health transitions.
func dockerContainer(ctx context.Context, client *http.Client, base, name string, res *Result) error {
	var c struct {
		Name         string `json:"Name"`
		RestartCount int    `json:"RestartCount"`
		State        struct {
			Status   string `json:"Status"`
			Running  bool   `json:"Running"`
			ExitCode int    `json:"ExitCode"`
			Health   *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := dockerGet(ctx, client, base+"/containers/"+name+"/json", &c); err != nil {
		return err
	}
	health := "none"
	if c.State.Health != nil && c.State.Health.Status != "" {
		health = c.State.Health.Status
	}
	res.Extra["container"] = strings.TrimPrefix(c.Name, "/")
	res.Extra["container.status"] = c.State.Status
	res.Extra["container.health"] = health
	res.Extra["container.running"] = strconv.FormatBool(c.State.Running)
	res.Extra["container.restartcount"] = strconv.Itoa(c.RestartCount)
	res.Extra["container.exitcode"] = strconv.Itoa(c.State.ExitCode)
	// fingerprint drives on_change: any status (or health) transition fires it.
	fp := c.State.Status
	if health != "none" {
		fp += "/" + health
	}
	res.Extra["fingerprint"] = fp
	return nil
}

// dockerClient builds an HTTP client for the daemon: a Unix-socket transport when
// cfg.Socket is set, otherwise TCP (egress-bound to cfg.Interface, TLS when
// requested). It returns the client and the base URL.
func dockerClient(cfg Config) (*http.Client, string, error) {
	if cfg.Socket != "" {
		socket := cfg.Socket
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialUnix(ctx, socket)
			},
		}
		// The host in the URL is a placeholder; the socket fixes the endpoint.
		return &http.Client{Transport: tr}, "http://docker", nil
	}
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := cfg.Port
	if port == 0 {
		port = 2375
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = BindDialer(cfg.Interface).DialContext
	scheme := "http"
	if mode := normalizeTLS(cfg.TLS); mode != "" {
		scheme = "https"
		tc := tlsClientConfig(host)
		if mode == "skip-verify" {
			tc.InsecureSkipVerify = true //nolint:gosec // operator chose tls: skip-verify
		}
		tr.TLSClientConfig = tc
	}
	return &http.Client{Transport: tr}, scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)), nil
}

// dockerGet performs a GET and decodes the JSON body into out. A non-200 status
// (e.g. 404 for an unknown container) is an error carrying the daemon's message.
func dockerGet(ctx context.Context, client *http.Client, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("docker: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 256<<10))
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("docker: invalid JSON response: %w", err)
	}
	return nil
}
