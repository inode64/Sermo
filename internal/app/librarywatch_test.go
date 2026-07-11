package app

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sermo/internal/config"
)

func TestLibraryWatchInterval(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want time.Duration
	}{
		{
			name: "default",
			cfg:  &config.Config{},
			want: DefaultEngineLibsInterval,
		},
		{
			name: "engine override",
			cfg: &config.Config{Global: config.Global{Raw: map[string]any{
				config.SectionEngine: map[string]any{config.EngineKeyLibsInterval: "7m"},
			}}},
			want: 7 * time.Minute,
		},
		{
			name: "library override",
			cfg: &config.Config{
				Global:       config.Global{Raw: map[string]any{config.SectionEngine: map[string]any{config.EngineKeyLibsInterval: "7m"}}},
				LibraryNames: []string{"demo"},
				Libraries: map[string]*config.Document{
					"demo": {Name: "demo", Body: map[string]any{"interval": "11m"}},
				},
			},
			want: 11 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := libraryWatchInterval(tt.cfg, "demo"); got != tt.want {
				t.Fatalf("libraryWatchInterval() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestLibrarySamplesDeferChangeUntilSampled(t *testing.T) {
	path := filepath.Join(t.TempDir(), "libdemo.so")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	samples := NewLibrarySamples()
	samples.Register(path)
	baseline := map[string]string{}

	if changed, err := libPathChanged(baseline, path, samples); err != nil || changed {
		t.Fatalf("unsampled library = changed:%t err:%v, want false nil", changed, err)
	}
	samples.Store(path)
	if changed, err := libPathChanged(baseline, path, samples); err != nil || changed {
		t.Fatalf("first sample = changed:%t err:%v, want false nil", changed, err)
	}
	if err := os.WriteFile(path, []byte("second value"), 0o644); err != nil {
		t.Fatal(err)
	}
	if changed, err := libPathChanged(baseline, path, samples); err != nil || changed {
		t.Fatalf("stale sample = changed:%t err:%v, want false nil", changed, err)
	}
	samples.Store(path)
	if changed, err := libPathChanged(baseline, path, samples); err != nil || !changed {
		t.Fatalf("updated sample = changed:%t err:%v, want true nil", changed, err)
	}
}
