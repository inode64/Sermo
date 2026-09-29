package app

import (
	"testing"
	"time"

	"sermo/internal/checks"
	"sermo/internal/config"
	"sermo/internal/mountctl"
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

// A manual disk I/O probe samples twice around its rate window, so its HTTP
// budget is two check timeouts plus the window.
func TestMaxOperationTimeoutCoversDiskIOProbeWindow(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"disk-io": map[string]any{
			"check": map[string]any{
				checks.CheckKeyType:    checks.CheckTypeDiskIO,
				checks.CheckKeyDevice:  "sda",
				checks.CheckKeyTimeout: "2m",
			},
		},
	})
	want := 2*2*time.Minute + defaultDiskIOProbeWindow
	if got := MaxOperationTimeout(cfg, 90*time.Second); got != want {
		t.Fatalf("MaxOperationTimeout = %v, want %v", got, want)
	}
}

// A button's own timeout bounds its command, so the HTTP write deadline must
// outlive the longest configured button.
func TestMaxOperationTimeoutRaisesForButtonTimeout(t *testing.T) {
	cfg := &config.Config{Services: map[string]*config.Document{
		"web": {Body: map[string]any{
			"name": "web",
			"buttons": map[string]any{
				"flush": map[string]any{"command": []any{"/usr/bin/true"}, "timeout": "5m"},
			},
		}},
	}}
	if got := MaxOperationTimeout(cfg, 90*time.Second); got != 5*time.Minute {
		t.Fatalf("MaxOperationTimeout = %v, want 5m so a 5m button can return over HTTP", got)
	}
}

// The web runs an also_apply cascade synchronously: every member operates in
// turn under its own timeout, and a blocked member is retried once after
// cascadeBlockedRetryDelay. The HTTP write deadline must cover the whole group.
func TestMaxOperationTimeoutRaisesForCascade(t *testing.T) {
	cfg := &config.Config{Services: map[string]*config.Document{
		"app":   {Body: map[string]any{"name": "app", "also_apply": []any{"db", "cache"}}},
		"db":    {Body: map[string]any{"name": "db", "stop_policy": map[string]any{"graceful_timeout": "120s"}}},
		"cache": {Body: map[string]any{"name": "cache"}},
	}}
	configured := 90 * time.Second
	dbTimeout := operation.ResolveTimeout(configured, cfg.Services["db"].Body)
	want := 2*(configured+configured+dbTimeout) + 3*cascadeBlockedRetryDelay
	if got := MaxOperationTimeout(cfg, configured); got != want {
		t.Fatalf("MaxOperationTimeout = %v, want %v for the app+db+cache cascade", got, want)
	}
}

// A web unmount may run every escalation step, each bounded by the mount
// command timeout, plus the configured TERM and KILL waits; the HTTP write
// deadline must outlive that whole budget.
func TestMaxOperationTimeoutRaisesForMountEscalation(t *testing.T) {
	cfg := cfgWithWatches(map[string]any{
		"mount-backup": map[string]any{
			"check": map[string]any{checks.CheckKeyType: checks.CheckTypeStorage, "path": "/mnt/backup", "mounted": true},
			"mount": map[string]any{
				"umount": map[string]any{"term_timeout": "60s", "kill_timeout": "10s"},
			},
		},
	})
	got := MaxOperationTimeout(cfg, 90*time.Second)
	want := 5*mountctl.DefaultCommandTimeout + 60*time.Second + 10*time.Second
	if got != want {
		t.Fatalf("MaxOperationTimeout = %v, want %v to cover the unmount escalation budget", got, want)
	}
}
