// Package servicemgr detects and controls the host service backend (systemd,
// OpenRC, ...) used to query status and start/stop/restart/reload services.
package servicemgr

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/execx"
)

const defaultDetectTimeout = 2 * time.Second

// Probe reads host state needed for backend detection.
type Probe interface {
	CommandExists(name string) bool
	PathExists(path string) bool
	ReadFile(path string) ([]byte, error)
}

// OSProbe reads the real host filesystem and PATH.
type OSProbe struct {
	Lookup execx.CommandLookup
}

// CommandExists reports whether name is available in PATH.
func (p OSProbe) CommandExists(name string) bool {
	lookup := p.Lookup
	if lookup == nil {
		lookup = execx.OSLookup{}
	}
	_, err := lookup.LookPath(name)
	return err == nil
}

// PathExists reports whether path exists.
func (OSProbe) PathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// ReadFile reads path from the host filesystem.
func (OSProbe) ReadFile(path string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

// Detector detects the active service manager backend.
type Detector struct {
	Runner  execx.Runner
	Probe   Probe
	Timeout time.Duration
}

// Detection describes the selected backend and how it was selected.
type Detection struct {
	Backend Backend
	Source  string
	Systemd BackendProbe
	OpenRC  BackendProbe
}

// BackendProbe contains probe data for one backend.
type BackendProbe struct {
	Available bool
	Active    bool
	State     string
}

// NewDetector returns a detector using the real host.
func NewDetector() Detector {
	return Detector{
		Runner:  execx.CommandRunner{},
		Probe:   OSProbe{},
		Timeout: defaultDetectTimeout,
	}
}

// Detect returns the requested backend or autodetects one when requested is auto.
func (d Detector) Detect(ctx context.Context, requested Backend) (Detection, error) {
	if d.Runner == nil {
		d.Runner = execx.CommandRunner{}
	}
	if d.Probe == nil {
		d.Probe = OSProbe{}
	}
	if d.Timeout <= 0 {
		d.Timeout = defaultDetectTimeout
	}

	systemd := d.probeSystemd(ctx)
	openrc := d.probeOpenRC(ctx)

	switch requested {
	case BackendSystemd:
		if !systemd.Available {
			return Detection{}, fmt.Errorf("requested backend systemd is not available")
		}
		return Detection{Backend: BackendSystemd, Source: "requested", Systemd: systemd, OpenRC: openrc}, nil
	case BackendOpenRC:
		if !openrc.Available {
			return Detection{}, fmt.Errorf("requested backend openrc is not available")
		}
		return Detection{Backend: BackendOpenRC, Source: "requested", Systemd: systemd, OpenRC: openrc}, nil
	case BackendAuto:
	default:
		return Detection{}, fmt.Errorf("unsupported backend %q", requested)
	}

	switch {
	case systemd.Available && !openrc.Available:
		return Detection{Backend: BackendSystemd, Source: "auto", Systemd: systemd, OpenRC: openrc}, nil
	case openrc.Available && !systemd.Available:
		return Detection{Backend: BackendOpenRC, Source: "auto", Systemd: systemd, OpenRC: openrc}, nil
	case systemd.Available && openrc.Available:
		if systemd.Active {
			return Detection{Backend: BackendSystemd, Source: "auto", Systemd: systemd, OpenRC: openrc}, nil
		}
		if openrc.Active {
			return Detection{Backend: BackendOpenRC, Source: "auto", Systemd: systemd, OpenRC: openrc}, nil
		}
		return Detection{}, errors.New("ambiguous backend: both systemd and openrc appear available; set --backend, SERMO_BACKEND or engine.backend")
	default:
		return Detection{}, errors.New("no supported init backend detected: systemd and openrc are unavailable")
	}
}

func (d Detector) probeSystemd(ctx context.Context) BackendProbe {
	if !d.Probe.CommandExists(cmdSystemctl) || !d.Probe.PathExists("/run/systemd/system") {
		return BackendProbe{}
	}

	result, _ := d.run(ctx, cmdSystemctl, "is-system-running")
	state := strings.TrimSpace(result.Stdout)
	if state == "" {
		state = strings.TrimSpace(result.Stderr)
	}

	active := isUsableSystemdState(state) || d.pid1Is("systemd")
	return BackendProbe{
		Available: state != "",
		Active:    active,
		State:     state,
	}
}

func (d Detector) probeOpenRC(ctx context.Context) BackendProbe {
	if !d.Probe.CommandExists(cmdRcService) {
		return BackendProbe{}
	}

	hasRunDir := d.Probe.PathExists("/run/openrc")
	rcStatusWorks := false
	state := ""
	if d.Probe.CommandExists(cmdRcStatus) {
		result, err := d.run(ctx, cmdRcStatus)
		state = strings.TrimSpace(result.Stdout)
		rcStatusWorks = err == nil
	}

	return BackendProbe{
		Available: hasRunDir || rcStatusWorks,
		Active:    hasRunDir && rcStatusWorks,
		State:     state,
	}
}

func (d Detector) run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	return execx.Run(ctx, d.Runner, d.Timeout, name, args...)
}

func (d Detector) pid1Is(name string) bool {
	data, err := d.Probe.ReadFile("/proc/1/comm")
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == name
}

func isUsableSystemdState(state string) bool {
	switch strings.TrimSpace(state) {
	case "running", "degraded":
		return true
	default:
		return false
	}
}
