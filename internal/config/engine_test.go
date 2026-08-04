package config

import (
	"testing"
	"time"
)

func TestEngineDuration(t *testing.T) {
	fallback := 45 * time.Second
	readers := []struct {
		name string
		key  string
		read func(*Config, time.Duration) time.Duration
	}{
		{name: "interval", key: keyInterval, read: EngineInterval},
		{name: "diagnostics interval", key: EngineKeyDiagnosticsInterval, read: EngineDiagnosticsInterval},
	}
	tests := []struct {
		name  string
		value any
		want  time.Duration
	}{
		{name: "nil config", want: fallback},
		{name: "missing raw", value: map[string]any(nil), want: fallback},
		{name: "missing engine", value: map[string]any{}, want: fallback},
		{name: "invalid value", value: "later", want: fallback},
		{name: "zero value", value: "0s", want: fallback},
		{name: "positive value", value: "15s", want: 15 * time.Second},
	}

	for _, reader := range readers {
		t.Run(reader.name, func(t *testing.T) {
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					var cfg *Config
					switch value := tc.value.(type) {
					case nil:
						if tc.name != "nil config" {
							cfg = &Config{}
						}
					case map[string]any:
						cfg = &Config{Global: Global{Raw: value}}
					default:
						cfg = &Config{Global: Global{Raw: map[string]any{
							SectionEngine: map[string]any{reader.key: value},
						}}}
					}
					if got := reader.read(cfg, fallback); got != tc.want {
						t.Fatalf("duration = %s, want %s", got, tc.want)
					}
				})
			}
		})
	}
}

func TestEngineServiceRestartNotice(t *testing.T) {
	cfg := &Config{Global: Global{Raw: map[string]any{
		SectionEngine: map[string]any{
			EngineKeyServiceRestartNotice: map[string]any{
				ServiceRestartNoticeKeyUptimeBelow: "5m",
				ServiceRestartNoticeKeyNotify:      []any{"wall"},
				ServiceRestartNoticeKeyMessage:     "${restart.service} restarted",
			},
		},
	}}}

	notice, configured := EngineServiceRestartNotice(cfg)
	if !configured {
		t.Fatal("restart notice should be configured")
	}
	if notice.UptimeBelow != 5*time.Minute || len(notice.Notify) != 1 || notice.Notify[0] != "wall" {
		t.Fatalf("notice = %+v", notice)
	}
	if notice.Subject != defaultServiceRestartNoticeSubject || notice.Message != "${restart.service} restarted" {
		t.Fatalf("notice templates = %+v", notice)
	}
	if _, configured := EngineServiceRestartNotice(&Config{}); configured {
		t.Fatal("missing restart notice must be disabled")
	}
}
