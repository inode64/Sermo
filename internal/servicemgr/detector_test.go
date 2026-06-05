package servicemgr

import (
	"context"
	"errors"
	"testing"

	"sermo/internal/execx"
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
	if detection.Backend != BackendSystemd {
		t.Fatalf("Detect() backend = %q, want %q", detection.Backend, BackendSystemd)
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
	if detection.Backend != BackendSystemd {
		t.Fatalf("Detect() backend = %q, want %q", detection.Backend, BackendSystemd)
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
	if detection.Backend != BackendOpenRC {
		t.Fatalf("Detect() backend = %q, want %q", detection.Backend, BackendOpenRC)
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
	if detection.Backend != BackendOpenRC {
		t.Fatalf("Detect() backend = %q, want %q", detection.Backend, BackendOpenRC)
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

func fakeDetector(commands, paths map[string]bool, results map[string]execx.Result) Detector {
	return fakeDetectorWithErrors(commands, paths, results, nil, nil)
}

func fakeDetectorWithErrors(commands, paths map[string]bool, results map[string]execx.Result, runnerErrors map[string]error, files map[string]string) Detector {
	return Detector{
		Runner: fakeRunner{results: results, errors: runnerErrors},
		Probe:  fakeProbe{commands: commands, paths: paths, files: files},
	}
}

type fakeRunner struct {
	results map[string]execx.Result
	errors  map[string]error
}

func (r fakeRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	key := name
	for _, arg := range args {
		key += " " + arg
	}
	result := r.results[key]
	if err := r.errors[key]; err != nil {
		return result, err
	}
	return result, nil
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
