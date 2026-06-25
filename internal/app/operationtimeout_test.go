package app

import (
	"testing"
	"time"

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
