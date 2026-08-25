package assist

import (
	"errors"
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
			if len(got) != len(tt.want) || got[0] != tt.want[0] {
				t.Fatalf("candidates = %v, want %v", got, tt.want)
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
