package app

import (
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/logfile"
)

func TestDiagnosticLogExport(t *testing.T) {
	path := t.TempDir() + "/diagnostics.log"
	file, err := logfile.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cfg := &config.Config{Global: config.Global{Raw: map[string]any{
		"engine": map[string]any{"interval": "30s"},
	}}}
	log := NewDiagnosticLog(cfg, nil, file, func() time.Time {
		return time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	})
	log.Export()
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
