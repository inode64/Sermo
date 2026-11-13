package checks

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"sermo/internal/process"
	"sermo/internal/servicemgr"
)

func TestNativeSystemdUnitState(t *testing.T) {
	for _, tt := range []struct {
		name          string
		loaded, state any
		result        bool
		readErr       error
		want          servicemgr.Status
		wantErr       bool
	}{
		{name: "active", loaded: "loaded", state: "active", want: servicemgr.StatusActive},
		{name: "inactive", loaded: "loaded", state: "inactive", want: servicemgr.StatusInactive},
		{name: "failed", loaded: "loaded", state: "failed", want: servicemgr.StatusFailed},
		{name: "not loaded", loaded: "not-found", want: servicemgr.StatusUnknown, wantErr: true},
		{name: "wrong type", loaded: "loaded", state: 42, want: servicemgr.StatusUnknown, wantErr: true},
		{name: "starting", loaded: "loaded", state: "activating", want: servicemgr.StatusUnknown, wantErr: true},
		{name: "bus failure", readErr: errors.New("bus unavailable"), want: servicemgr.StatusUnknown, wantErr: true},
		{name: "result", loaded: "loaded", state: "success", result: true, want: "success"},
		{name: "missing result", loaded: "loaded", result: true, want: servicemgr.StatusUnknown, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			read := func(_ context.Context, unit, iface, property string) (any, error) {
				if unit != "app.service" {
					t.Fatalf("unit=%q", unit)
				}
				if property == systemdPropertyLoadState {
					return tt.loaded, tt.readErr
				}
				if tt.result && iface != systemdServiceInterface {
					t.Fatalf("result interface %q", iface)
				}
				return tt.state, tt.readErr
			}
			sample := systemdUnitState
			if tt.result {
				sample = systemdUnitResult
			}
			state, err := sample(t.Context(), "app", read)
			if state != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got %s, %v; want %s, error=%v", state, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestOpenRCDaemonObservation(t *testing.T) {
	for _, state := range []string{process.StateRunning, process.StateAbsent, process.StateUnknown} {
		t.Run(state, func(t *testing.T) {
			running, err := openRCDaemonRunning(t.Context(), servicemgr.ProcInfo{Cmd: "^worker$"}, func(context.Context, []string, string, *regexp.Regexp) string { return state }, nil)
			if running != (state == process.StateRunning) || (err != nil) != (state == process.StateUnknown) {
				t.Fatalf("got %v, %v", running, err)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "app.pid")
	if err := os.WriteFile(path, []byte("123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, present := range []bool{false, true} {
		running, err := openRCDaemonRunning(t.Context(), servicemgr.ProcInfo{Pidfile: path}, nil, func(pid int) (process.Identity, bool, error) {
			if pid != 123 {
				t.Fatalf("pid=%d", pid)
			}
			return process.Identity{PID: pid}, present, nil
		})
		if running != present || err != nil {
			t.Fatalf("got %v, %v", running, err)
		}
	}
}

func TestAnyUnitState(t *testing.T) {
	for _, tt := range []struct {
		name    string
		states  []servicemgr.Status
		want    servicemgr.Status
		wantErr bool
	}{
		{"socket active", []servicemgr.Status{servicemgr.StatusInactive, servicemgr.StatusActive}, servicemgr.StatusActive, false},
		{"missing service and active socket", []servicemgr.Status{servicemgr.StatusUnknown, servicemgr.StatusActive}, servicemgr.StatusActive, false},
		{"missing service and inactive socket", []servicemgr.Status{servicemgr.StatusUnknown, servicemgr.StatusInactive}, servicemgr.StatusInactive, true},
		{"both inactive", []servicemgr.Status{servicemgr.StatusInactive, servicemgr.StatusInactive}, servicemgr.StatusInactive, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			index := 0
			state, err := anyUnitState(t.Context(), []string{"app.service", "app.socket"}, "active", func(context.Context, string) (servicemgr.Status, error) {
				state := tt.states[index]
				index++
				if state == servicemgr.StatusUnknown {
					return state, errors.New("unit not loaded")
				}
				return state, nil
			})
			if state != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("%s %v", state, err)
			}
		})
	}
}

func TestServiceCheckBuildRejectsInvalidFields(t *testing.T) {
	for _, tt := range []struct {
		name   string
		fields map[string]any
	}{
		{"invalid state", map[string]any{"expect": "actve"}},
		{"invalid explicit state", map[string]any{"service": "app", "backend": "systemd", "expect": "actve"}},
		{"result without target", map[string]any{"expect": "active", "expect_result": "success"}},
		{"state and result", map[string]any{"service": "app", "backend": "systemd", "expect": "active", "expect_result": "success"}},
		{"empty result", map[string]any{"service": "app", "backend": "systemd", "expect_result": ""}},
		{"invalid target list", map[string]any{"service": []any{"app", 42}, "backend": "systemd", "expect": "active"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tt.fields[CheckKeyType] = CheckTypeService
			_, err := BuildInline("unit", tt.fields, Deps{Status: func(context.Context) (servicemgr.Status, error) {
				t.Fatal("invalid check reached the backend")
				return servicemgr.StatusUnknown, nil
			}})
			if err == nil {
				t.Fatalf("accepted invalid service fields: %v", tt.fields)
			}
		})
	}
}
