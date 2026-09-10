package servicemgr

import (
	"context"
	"errors"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

func TestDetectSystemdRunning(t *testing.T) {
	detector := fakeDetector(
		map[string]bool{"systemctl": true},
		map[string]bool{"/run/systemd/system": true},
		map[string]execx.Result{"systemctl is-system-running": {Stdout: "running\n"}},
	)

	detection, err := detector.Detect(context.Background(), BackendAuto)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if detection != BackendSystemd {
		t.Fatalf("Detect() backend = %q, want %q", detection, BackendSystemd)
	}
}

func TestDetectSystemdDegradedIsUsable(t *testing.T) {
	detector := fakeDetector(
		map[string]bool{"systemctl": true},
		map[string]bool{"/run/systemd/system": true},
		map[string]execx.Result{"systemctl is-system-running": {Stdout: "degraded\n", ExitCode: 1}},
	)

	detection, err := detector.Detect(context.Background(), BackendAuto)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if detection != BackendSystemd {
		t.Fatalf("Detect() backend = %q, want %q", detection, BackendSystemd)
	}
}

func TestDetectOpenRC(t *testing.T) {
	detector := fakeDetector(
		map[string]bool{"rc-service": true, "rc-status": true},
		map[string]bool{"/run/openrc": true},
		map[string]execx.Result{"rc-status": {Stdout: "Runlevel: default\n"}},
	)

	detection, err := detector.Detect(context.Background(), BackendAuto)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if detection != BackendOpenRC {
		t.Fatalf("Detect() backend = %q, want %q", detection, BackendOpenRC)
	}
}

func TestDetectBothPresentPrefersActiveOpenRC(t *testing.T) {
	detector := fakeDetector(
		map[string]bool{"systemctl": true, "rc-service": true, "rc-status": true},
		map[string]bool{"/run/systemd/system": true, "/run/openrc": true},
		map[string]execx.Result{
			"systemctl is-system-running": {Stdout: "starting\n"},
			"rc-status":                   {Stdout: "Runlevel: default\n"},
		},
	)

	detection, err := detector.Detect(context.Background(), BackendAuto)
	if err != nil {
		t.Fatalf("Detect() error = %v", err)
	}
	if detection != BackendOpenRC {
		t.Fatalf("Detect() backend = %q, want %q", detection, BackendOpenRC)
	}
}

func TestDetectBothPresentAmbiguous(t *testing.T) {
	runnerErrors := map[string]error{"rc-status": errors.New("rc-status failed")}
	detector := fakeDetectorWithErrors(
		map[string]bool{"systemctl": true, "rc-service": true, "rc-status": true},
		map[string]bool{"/run/systemd/system": true, "/run/openrc": true},
		map[string]execx.Result{
			"systemctl is-system-running": {Stdout: "starting\n"},
			"rc-status":                   {ExitCode: 1},
		},
		runnerErrors,
		nil,
	)

	_, err := detector.Detect(context.Background(), BackendAuto)
	if err == nil {
		t.Fatal("Detect() error = nil, want ambiguous error")
	}
}

func TestDetectNeitherPresent(t *testing.T) {
	detector := fakeDetector(nil, nil, nil)

	_, err := detector.Detect(context.Background(), BackendAuto)
	if err == nil {
		t.Fatal("Detect() error = nil, want unsupported backend error")
	}
}

func TestRequestedBackendMustBeAvailable(t *testing.T) {
	detector := fakeDetector(nil, nil, nil)

	_, err := detector.Detect(context.Background(), BackendSystemd)
	if err == nil {
		t.Fatal("Detect(systemd) error = nil, want unavailable error")
	}
}

func TestRequestedBackendProbesOnlyThatBackend(t *testing.T) {
	runner := &execxtest.Runner{ByLine: map[string]execx.Result{
		"systemctl is-system-running": {Stdout: "running\n"},
	}}
	detector := Detector{
		Runner: runner,
		Probe: fakeProbe{
			commands: map[string]bool{cmdSystemctl: true, cmdRcService: true, cmdRcStatus: true},
			paths:    map[string]bool{systemdRuntimeDir: true, openRCRuntimeDir: true},
		},
	}

	detection, err := detector.Detect(context.Background(), BackendSystemd)
	if err != nil || detection != BackendSystemd {
		t.Fatalf("Detect(systemd) = %+v, %v", detection, err)
	}
	if runner.Ran(cmdRcStatus) {
		t.Fatal("explicit systemd detection must not probe OpenRC")
	}
}

func TestRequestedOpenRCProbesOnlyThatBackend(t *testing.T) {
	runner := &execxtest.Runner{}
	detector := Detector{
		Runner: runner,
		Probe: fakeProbe{
			commands: map[string]bool{cmdSystemctl: true, cmdRcService: true, cmdRcStatus: true},
			paths:    map[string]bool{systemdRuntimeDir: true, openRCRuntimeDir: true},
		},
	}

	detection, err := detector.Detect(context.Background(), BackendOpenRC)
	if err != nil || detection != BackendOpenRC {
		t.Fatalf("Detect(openrc) = %+v, %v", detection, err)
	}
	if runner.Ran(cmdSystemctl) {
		t.Fatal("explicit OpenRC detection must not probe systemd")
	}
}

func fakeDetector(commands, paths map[string]bool, results map[string]execx.Result) Detector {
	return fakeDetectorWithErrors(commands, paths, results, nil, nil)
}

func fakeDetectorWithErrors(commands, paths map[string]bool, results map[string]execx.Result, runnerErrors map[string]error, files map[string]string) Detector {
	return Detector{
		Runner: &execxtest.Runner{ByLine: results, Errs: runnerErrors},
		Probe:  fakeProbe{commands: commands, paths: paths, files: files},
	}
}

type fakeProbe struct {
	commands map[string]bool
	paths    map[string]bool
	files    map[string]string
}

func (p fakeProbe) CommandExists(name string) bool {
	return p.commands[name]
}

func (p fakeProbe) PathExists(path string) bool {
	return p.paths[path]
}

func (p fakeProbe) ReadFile(path string) ([]byte, error) {
	value, ok := p.files[path]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(value), nil
}
