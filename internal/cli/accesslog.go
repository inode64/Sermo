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
	accessSourceCLI   = "cli"
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
		cliJSONKeyTime:    time.Now().UTC().Format(time.RFC3339),
		cliJSONKeySource:  accessSourceCLI,
		cliJSONKeyActor:   actor,
		cliJSONKeyCommand: command,
		cliJSONKeyTarget:  target,
		cliJSONKeyStatus:  status,
		cliJSONKeyMessage: message,
	})
}
