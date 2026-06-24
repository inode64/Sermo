package app

import (
	"testing"
	"time"

	"sermo/internal/config"
	"sermo/internal/diag"
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
	log := NewDiagnosticLog(cfg, nil, nil, file, func() time.Time {
		return time.Date(2026, 6, 24, 12, 0, 0, 0, time.UTC)
	})
	log.Export()
	if err := file.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOperationSlotDiagFindings(t *testing.T) {
	if got := operationSlotDiagFindings(0, 2); len(got) != 0 {
		t.Fatalf("got %+v", got)
	}
	if got := operationSlotDiagFindings(1, 2); len(got) != 1 || got[0].Level != diag.LevelInfo {
		t.Fatalf("got %+v", got)
	}
	if got := operationSlotDiagFindings(2, 2); len(got) != 1 || got[0].Level != diag.LevelWarning {
		t.Fatalf("got %+v", got)
	}
}
