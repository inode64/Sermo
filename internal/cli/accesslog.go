package cli

import (
	"os"
	"time"

	"sermo/internal/config"
	"sermo/internal/logfile"
)

const (
	accessStatusOK    = "ok"
	accessStatusError = "error"
)

const (
	accessCommandStateCompact = "state compact"
	accessCommandLockAcquire  = "lock acquire"
	accessCommandLockRelease  = "lock release"
	accessCommandLockWrap     = "lock wrap"
	accessCommandEventsClear  = "events clear"
	accessCommandDaemonReload = "daemon reload"
)

// recordAccess appends one CLI access record when engine.access is configured.
func (a App) recordAccess(cfg *config.Config, command, target, status, message string) {
	if cfg == nil {
		return
	}
	path := config.EngineLogPath(cfg, "access")
	if path == "" {
		return
	}
	w, err := logfile.Open(path)
	if err != nil {
		return
	}
	defer w.Close()

	actor := os.Getenv("USER")
	if actor == "" {
		actor = "-"
	}
	_ = w.Write(map[string]any{
		"time":    time.Now().UTC().Format(time.RFC3339),
		"source":  "cli",
		"actor":   actor,
		"command": command,
		"target":  target,
		"status":  status,
		"message": message,
	})
}
