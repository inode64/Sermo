package assist

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestDetectCandidates(t *testing.T) {
	tests := []struct {
		name    string
		detect  func() ([]string, error)
		want    []string
		wantErr string
	}{
		{name: "unavailable", wantErr: "target detection is unavailable"},
		{name: "failure", detect: func() ([]string, error) { return nil, errors.New("probe failed") }, wantErr: "detect targets: probe failed"},
		{name: "empty", detect: func() ([]string, error) { return []string{}, nil }, want: []string{}},
		{name: "success", detect: func() ([]string, error) { return []string{"one"}, nil }, want: []string{"one"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detectCandidates(tt.detect, "target detection is unavailable", "detect targets")
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("detectCandidates: %v", err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("candidates = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHostAssistantsReportNoCandidates(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Prompt, Env) (Result, error)
		env  Env
		want string
	}{
		{name: "volume", run: volumeAssistant{}.Run, env: Env{Volumes: func() ([]Volume, error) { return nil, nil }}, want: "no storage volumes found to monitor"},
		{name: "mount", run: mountAssistant{}.Run, env: Env{Mounts: func() ([]MountCandidate, error) { return nil, nil }}, want: "no fstab mount points were detected on this host"},
		{name: "net", run: netAssistant{}.Run, env: Env{Ifaces: func() ([]Iface, error) { return nil, nil }}, want: "no non-loopback network interfaces found"},
		{name: "uplink", run: uplinkAssistant{}.Run, env: Env{Ifaces: func() ([]Iface, error) { return nil, nil }}, want: "no candidate interfaces found"},
		{name: "service", run: serviceAssistant{}.Run, env: Env{CatalogServices: func() ([]ServiceCandidate, error) { return nil, nil }}, want: "no active services were detected on this host"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPrompt(strings.NewReader(""), &strings.Builder{})
			_, err := tt.run(p, tt.env)
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestHostAssistantsReportUnavailableDetection(t *testing.T) {
	tests := []struct {
		name string
		run  func(*Prompt, Env) (Result, error)
		want string
	}{
		{name: "volume", run: volumeAssistant{}.Run, want: "volume detection is unavailable"},
		{name: "mount", run: mountAssistant{}.Run, want: "mount detection is unavailable"},
		{name: "net", run: netAssistant{}.Run, want: "interface detection is unavailable"},
		{name: "uplink", run: uplinkAssistant{}.Run, want: "interface detection is unavailable"},
		{name: "service", run: serviceAssistant{}.Run, want: "service detection is unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := NewPrompt(strings.NewReader(""), &strings.Builder{})
			_, err := tt.run(p, Env{})
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}
