package config

import (
	"testing"
	"time"
)

func TestEngineInterval(t *testing.T) {
	fallback := 45 * time.Second
	tests := []struct {
		name string
		cfg  *Config
		want time.Duration
	}{
		{name: "nil config", cfg: nil, want: fallback},
		{name: "missing raw", cfg: &Config{}, want: fallback},
		{name: "missing engine", cfg: &Config{Global: Global{Raw: map[string]any{}}}, want: fallback},
		{name: "invalid interval", cfg: &Config{Global: Global{Raw: map[string]any{
			"engine": map[string]any{"interval": "later"},
		}}}, want: fallback},
		{name: "zero interval", cfg: &Config{Global: Global{Raw: map[string]any{
			"engine": map[string]any{"interval": "0s"},
		}}}, want: fallback},
		{name: "positive interval", cfg: &Config{Global: Global{Raw: map[string]any{
			"engine": map[string]any{"interval": "15s"},
		}}}, want: 15 * time.Second},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := EngineInterval(tc.cfg, fallback); got != tc.want {
				t.Fatalf("EngineInterval() = %s, want %s", got, tc.want)
			}
		})
	}
}
