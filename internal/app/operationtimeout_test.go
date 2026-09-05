package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/operation"
)

func TestMaxOperationTimeoutRaisesForStopPolicy(t *testing.T) {
	cfg := &config.Config{
		Services: map[string]*config.Document{
			"db": {Body: map[string]any{
				"name": "db",
				"stop_policy": map[string]any{
					"graceful_timeout": "120s",
				},
			}},
		},
	}
	got := MaxOperationTimeout(cfg, 90*time.Second)
	want := operation.ResolveTimeout(90*time.Second, cfg.Services["db"].Body)
	if got != want {
		t.Fatalf("MaxOperationTimeout = %v, want %v", got, want)
	}
	if got <= 90*time.Second {
		t.Fatalf("expected stop_policy to raise timeout above 90s, got %v", got)
	}
}

func TestMaxOperationTimeoutDefaultWhenNoServices(t *testing.T) {
	got := MaxOperationTimeout(&config.Config{}, 0)
	if got != operation.DefaultOperationTimeout {
		t.Fatalf("got %v, want default %v", got, operation.DefaultOperationTimeout)
	}
}

func TestMaxOperationTimeoutRaisesForWatchProbe(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"disk": map[string]any{
			"check": map[string]any{
				checks.CheckKeyType:    checks.CheckTypeHdparm,
				checks.CheckKeyDevice:  "/dev/sda",
				checks.CheckKeyTimeout: "5m",
				"read":                 map[string]any{"op": "<", "value": 20},
			},
		},
	})
	got := MaxOperationTimeout(cfg, 90*time.Second)
	if got != 5*time.Minute {
		t.Fatalf("MaxOperationTimeout = %v, want 5m so a 5m watch probe can return over HTTP", got)
	}
}

func TestMaxOperationTimeoutIgnoresDisabledWatchProbe(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"disk": map[string]any{
			"enabled": false,
			"check": map[string]any{
				checks.CheckKeyType:    checks.CheckTypeHdparm,
				checks.CheckKeyDevice:  "/dev/sda",
				checks.CheckKeyTimeout: "5m",
				"read":                 map[string]any{"op": "<", "value": 20},
			},
		},
	})
	got := MaxOperationTimeout(cfg, 90*time.Second)
	if got != 90*time.Second {
		t.Fatalf("MaxOperationTimeout = %v, want 90s; a disabled watch must not raise the HTTP write deadline", got)
	}
}

func TestMaxOperationTimeoutIgnoresNonProbeableWatch(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"clock": map[string]any{
			"check": map[string]any{
				checks.CheckKeyType:    checks.CheckTypeClock,
				checks.CheckKeyTimeout: "5m",
			},
		},
	})
	got := MaxOperationTimeout(cfg, 90*time.Second)
	if got != 90*time.Second {
		t.Fatalf("MaxOperationTimeout = %v, want 90s; a non-probeable watch must not raise the HTTP write deadline", got)
	}
}

func TestMaxOperationTimeoutIgnoresWatchInterval(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"disk": map[string]any{
			"interval": "5m",
			"check": map[string]any{
				checks.CheckKeyType:   checks.CheckTypeHdparm,
				checks.CheckKeyDevice: "/dev/sda",
				"read":                map[string]any{"op": "<", "value": 20},
			},
		},
	})
	got := MaxOperationTimeout(cfg, 90*time.Second)
	if got != 90*time.Second {
		t.Fatalf("MaxOperationTimeout = %v, want 90s; watch interval is the poll cadence, not the probe budget", got)
	}
}
