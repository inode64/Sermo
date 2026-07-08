package conn

import (
	"context"
	"strconv"

	"sermo/internal/dockerctl"
)

func init() { Register(dockerProtocol{}) }

// DefaultDockerSocket is Docker Engine's local Unix API socket.
const DefaultDockerSocket = dockerctl.DefaultSocket

// DockerContainerStatusRunning is the running status emitted in
// ExtraKeyContainerStatus.
const DockerContainerStatusRunning = dockerctl.ContainerStatusRunning

// dockerProtocol probes a Docker Engine daemon over its HTTP API, by default on
// the local Unix socket /run/docker.sock (set `host` for a TCP daemon, with
// `tls` for 2376). It GETs /info — proving the daemon is up — and exposes the
// container counts (total/running/paused/stopped), image count and daemon warning
// count as variables, plus the engine version. With a `container` selected it also
// reads that container's state and health. Operators alert on any of these with
// `expect` (e.g. containers.running) or on a container's state change with
// `on_change`; the engine version drives `on_version_change`.
type dockerProtocol struct{}

// Name returns the canonical type token.
func (dockerProtocol) Name() string { return ProtocolNameDocker }

// DefaultPort is Docker's plaintext TCP port (use 2376 with tls). Ignored when
// probing the default Unix socket.
func (dockerProtocol) DefaultPort() int { return dockerctl.DefaultPort }

// RequiresUser reports that no user is required (the socket/TCP endpoint is
// authorized by the OS / TLS client cert, not a username).
func (dockerProtocol) RequiresUser() bool { return false }

// Probe reads /info and, when a container is selected, that container's state.
func (dockerProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	client, err := dockerClient(cfg)
	if err != nil {
		return Result{}, err
	}
	defer client.CloseIdleConnections()

	info, err := client.Info(ctx)
	if err != nil {
		return Result{}, err
	}
	res := Result{Version: info.ServerVersion, Extra: map[string]string{
		ExtraKeyDockerContainers: strconv.Itoa(info.Containers),
		ExtraKeyDockerRunning:    strconv.Itoa(info.ContainersRunning),
		ExtraKeyDockerPaused:     strconv.Itoa(info.ContainersPaused),
		ExtraKeyDockerStopped:    strconv.Itoa(info.ContainersStopped),
		ExtraKeyDockerImages:     strconv.Itoa(info.Images),
		ExtraKeyDockerWarnings:   strconv.Itoa(len(info.Warnings)),
	}}

	if name := cfg.Query; name != "" {
		container, err := client.Inspect(ctx, name)
		if err != nil {
			return Result{}, err
		}
		dockerContainer(container, &res)
	}
	return res, nil
}

// dockerContainer reads one container's state/health into res.Extra and sets the
// change fingerprint so on_change tracks its state/health transitions.
func dockerContainer(c dockerctl.Container, res *Result) {
	health := c.HealthStatus()
	res.Extra[ExtraKeyContainer] = c.ContainerName()
	res.Extra[ExtraKeyContainerStatus] = c.State.Status
	res.Extra[ExtraKeyContainerHealth] = health
	res.Extra[ExtraKeyContainerRunning] = strconv.FormatBool(c.State.Running)
	res.Extra[ExtraKeyContainerRestarts] = strconv.Itoa(c.RestartCount)
	res.Extra[ExtraKeyContainerExitCode] = strconv.Itoa(c.State.ExitCode)
	// fingerprint drives on_change: any status (or health) transition fires it.
	fp := c.State.Status
	if health != dockerctl.HealthStatusNone {
		fp += "/" + health
	}
	res.Extra[ExtraKeyFingerprint] = fp
}

// dockerClient builds an HTTP client for the daemon: a Unix-socket transport when
// cfg.Socket is set, otherwise TCP (egress-bound to cfg.Interface, TLS when
// requested).
func dockerClient(cfg Config) (*dockerctl.Client, error) {
	spec := dockerctl.Spec{Socket: cfg.Socket, Host: cfg.Host, Port: cfg.Port, TLS: cfg.TLS}
	if cfg.Socket == "" {
		spec.DialContext = BindDialer(cfg.Interface).DialContext
	}
	return dockerctl.NewClient(spec)
}
